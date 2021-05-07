package deploy

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-07-01/compute"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/Azure/go-autorest/autorest/to"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	timeoutMinutes = 20
)

type azureSession struct {
	ResourceGroupName string
	ScaleSetName      string
	SubscriptionID    string
	Authorizer        *autorest.Authorizer
}

// Attaches the session's authorizer to a new instance of the VM Scale Set client
func (s *azureSession) getVMSSClient() compute.VirtualMachineScaleSetsClient {
	client := compute.NewVirtualMachineScaleSetsClient(s.SubscriptionID)
	client.Authorizer = *s.Authorizer
	return client
}

// Attaches the session's authorizer to a new instance of the VMSS VM client
func (s *azureSession) getVMSSVMClient() compute.VirtualMachineScaleSetVMsClient {
	client := compute.NewVirtualMachineScaleSetVMsClient(s.SubscriptionID)
	client.Authorizer = *s.Authorizer
	return client
}

// Iterates through the instances within a Scale Set. If 'protect' is true,
// we apply scale-in protection to any instances which are the latest model
// (created using the most recent VMSS configuration). When false, we remove
// protection from all instances.
//
// Returns a slice of futures, which we can optionally await to block further
// operations until we know the operations have completed.
func (s *azureSession) setVMProtection(ctx context.Context, protect bool) ([]compute.VirtualMachineScaleSetVMsUpdateFuture, error) {
	var futures []compute.VirtualMachineScaleSetVMsUpdateFuture
	var filter string

	client := s.getVMSSVMClient()

	if protect {
		filter = "properties/latestModelApplied eq true"
		log.Info("Applying scale-in protection to new instances")
	} else {
		// Leave this defaulted to an empty string for now
		// This will un-protect ALL members of the VMSS upon completion
		// filter = "properties/latestModelApplied eq false"
		log.Info("Removing scale-in protection from Scale Set instances")
	}

	for vms, err := client.ListComplete(ctx, s.ResourceGroupName, s.ScaleSetName, filter, "", ""); vms.NotDone(); err = vms.Next() {
		if err != nil {
			return futures, err
		}

		vm := vms.Value()

		vm.ProtectionPolicy = &compute.VirtualMachineScaleSetVMProtectionPolicy{
			ProtectFromScaleIn:         &protect,
			ProtectFromScaleSetActions: to.BoolPtr(false),
		}

		future, err := client.Update(
			ctx,
			s.ResourceGroupName,
			s.ScaleSetName,
			*vm.InstanceID,
			vm,
		)
		if err != nil {
			return futures, err
		}

		futures = append(futures, future)
	}

	return futures, nil
}

// Helper function, accepts a slice of VMSS VM Update futures and
// spawns a goroutine to poll for success on each. Upon completion
// of each, logs the modified VM's resource name. Upon completion
// of all futures, returns.
func (s *azureSession) awaitVMFutures(ctx context.Context, futures []compute.VirtualMachineScaleSetVMsUpdateFuture) error {
	var wg sync.WaitGroup
	var err error
	e := make(chan error, 1)

	// 'Fork' the upstream timed context.
	// If the upstream context is canceled, these will die, too.
	// Otherwise, an error in the 'inner' context will cancel the
	// other API calls and pass the error out via channel.
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for _, future := range futures {
		client := s.getVMSSVMClient()

		wg.Add(1)
		go func(client compute.VirtualMachineScaleSetVMsClient, future compute.VirtualMachineScaleSetVMsUpdateFuture) {
			defer wg.Done()

			innerErr := future.WaitForCompletionRef(subCtx, client.Client)
			if innerErr != nil {
				e <- innerErr
				cancel()
				return
			}

			res, innerErr := future.Result(client)
			if innerErr != nil {
				e <- innerErr
				cancel()
				return
			}

			log.Infof("Modified VM: %s", *res.Name)
		}(client, future)
	}

	wg.Wait()
	if subCtx.Err() != nil {
		err = <-e
	}
	close(e)
	return err // Default nil if the channel was empty
}

// Adjusts the desired capacity of the chosen scale set by a given factor.
// Blocks execution until all VMSS instances have reported success.
//
// Instances will not report success until VM Extensions scripts have returned
// with an exit code of 0.
func (s *azureSession) scaleVMSSByFactor(ctx context.Context, factor float64) error {
	client := s.getVMSSClient()

	scaleSet, err := client.Get(ctx, s.ResourceGroupName, s.ScaleSetName)
	if err != nil {
		return err
	}

	// Ick
	newCapacity := int64(math.Floor(float64(*scaleSet.Sku.Capacity) * factor))

	log.Infof("Scaling VMSS %s to %d instances", *scaleSet.Name, newCapacity)

	future, err := client.Update(
		ctx,
		s.ResourceGroupName,
		s.ScaleSetName,
		compute.VirtualMachineScaleSetUpdate{
			Sku: &compute.Sku{
				Name:     scaleSet.Sku.Name,
				Tier:     scaleSet.Sku.Tier,
				Capacity: &newCapacity,
			},
		},
	)
	if err != nil {
		return err
	}

	err = future.WaitForCompletionRef(ctx, client.Client)
	if err != nil {
		return err
	}

	return nil
}

func (s *azureSession) instancesNeedUpgrade(ctx context.Context) (bool, error) {
	client := s.getVMSSVMClient()
	const filter string = "properties/latestModelApplied eq false"

	vms, err := client.ListComplete(ctx, s.ResourceGroupName, s.ScaleSetName, filter, "", "")
	if err != nil {
		return false, err
	}
	return vms.NotDone(), nil
}

func (s *azureSession) awaitInstanceHealthChecks(ctx context.Context) error {
	client := s.getVMSSVMClient()
	const healthy string = "HealthState/healthy"

	log.Info("Waiting for instance health checks to pass")
	// Rely on the context timeout to kill this if we run too long
	for true {
		instanceUnhealthy := false
		for vms, err := client.ListComplete(ctx, s.ResourceGroupName, s.ScaleSetName, "properties/latestModelApplied eq true", "", "instanceView"); vms.NotDone(); err = vms.Next() {
			if err != nil {
				return err
			}

			vm := vms.Value()
			if *vm.InstanceView.VMHealth.Status.Code != healthy {
				instanceUnhealthy = true
				break
			}
		}

		if instanceUnhealthy {
			log.Info("VM instances do not yet report healthy. Backing off and retrying in 30 seconds.")
			time.Sleep(30 * time.Second)
		} else {
			break
		}
	}
	return nil
}

// Initializes a new azureSession struct. Mostly used to get
// rid of unnecessary variable passing and allow the chosen
// authorizer to be easily replaced.
func newSession(subscription string, rg string, scaleSet string) (*azureSession, error) {
	authorizer, err := auth.NewAuthorizerFromCLI()
	if err != nil {
		return &azureSession{}, err
	}

	return &azureSession{
		SubscriptionID:    subscription,
		ResourceGroupName: rg,
		ScaleSetName:      scaleSet,
		Authorizer:        &authorizer,
	}, nil
}

// Run initializes a session and executes the upgrade operation
func Run(cmd *cobra.Command, args []string) {
	log.Info("Initializing Cluster Blue/Green Upgrade")

	// Maybe we should let users override the timeout with a CLI flag?
	ctx, cancel := context.WithTimeout(context.Background(), timeoutMinutes*time.Minute)
	defer cancel() // In the event we return/exit early, stop all children of this context

	sess, err := newSession(
		cmd.Flags().Lookup("subscription-id").Value.String(),
		cmd.Flags().Lookup("resource-group").Value.String(),
		cmd.Flags().Lookup("vm-scale-set").Value.String(),
	)
	if err != nil {
		log.Fatal(err)
	}

	cont, err := sess.instancesNeedUpgrade(ctx)
	if err != nil {
		log.Fatal(err)
	} else if !cont {
		log.Info("All VMs report up-to-date. Nothing to do.")
		return
	}

	if err = sess.scaleVMSSByFactor(ctx, 2); err != nil {
		log.Fatal(err)
	}

	skip, _ := cmd.Flags().GetBool("skip-health-check")
	if !skip {
		err = sess.awaitInstanceHealthChecks(ctx)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		log.Warn("Health checks skipped by user")
	}

	// Protect newly-created instances
	scaleOutFutures, err := sess.setVMProtection(ctx, true)
	if err != nil {
		log.Fatal(err)
	}

	if err = sess.awaitVMFutures(ctx, scaleOutFutures); err != nil {
		log.Fatal(err)
	}

	// Halve VMSS Capacity
	if err = sess.scaleVMSSByFactor(ctx, 0.5); err != nil {
		log.Fatal(err)
	}

	// Un-protect instances
	scaleInFutures, err := sess.setVMProtection(ctx, false)
	if err != nil {
		log.Fatal(err)
	}

	if err = sess.awaitVMFutures(ctx, scaleInFutures); err != nil {
		log.Fatal(err)
	}
}
