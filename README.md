# Azure VM Scale Set - Blue/Green Upgrade Utility 
This utility performs a blue/green upgrade on an Azure VM Scale Set. This is useful when a VMSS is set to Manual upgrade mode, and the user wishes to perform an update by scaling in a set of replacement nodes, rather than asking Azure to remediate a set of already-running instances. The upgrade is carried out performing the following steps:

- Verify VM Scale Set instances require update. We assume a given scale set requires update if any instances report they are not the 'latest model'.
- Double the capacity of the Scale Set
- (Optional) Wait for all VMSS instance health checks to succeed
- Apply scale-in protection to all instances in the replacement set
- Reduce the capacity of the Scale Set by half
- Remove scale-in protection from the remaining instances

The entire operation is subject to a 20-minute global timeout. In the even this timeout is exceeded, or an error is encountered anywhere else in the process, we eagerly bail out of the operation and leave the scale set in place. This is done so that the failed instances are available for root cause analysis, and to protect applications like [HashiCorp Consul](https://github.com/hashicorp/consul) that may experience data loss if the outgoing instance set is inadvertently stopped before a migration has been fully verified.

## Configuration
Currently, we assume the user has a valid set of credentials provided by Azure CLI. These can be generated using the following commands:
- Azure AD User: `az login`
- Azure AD Service Principal: `az login --service-principal`
- Managed Service Identity: `az login --identity`

The Azure Subscription, Resource Group, and VM Scale Set name must be provided at run time. The relevant flags can be found by running the following command:

```
$ ./azure-cluster-upgrade --help
Interacts with the Azure API to perform a blue/green deployment.

Expects a Virtual Machine Scale Set whose configuration has recently been updated.
Expands the chosen scale set by a factor of two, and once all VMs have entered the
'Running' state, protects the replacement instances and reduces Scale Set capacity
to its original value.

Usage:
  azure-cluster-upgrade [flags]

Flags:
  -h, --help                     help for azure-cluster-upgrade
  -r, --resource-group string    Resource Group name
      --skip-health-check        Skip testing instance health checks
  -s, --subscription-id string   Subscription ID
  -v, --vm-scale-set string      Virtual Machine Scale Set name
```
