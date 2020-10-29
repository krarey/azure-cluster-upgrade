# v1.0.1
- Migrated builds from go-dep to Go Modules and updated library versions. No significant functionality changes.

# v1.0.0
- Will now test whether any VMSS instances report that they are not the latest model. If all instances report they are the latest model, exit early with a successful status code. This lets us stay idempotent when running in CI/CD
- Added support for VM health checks. The script will wait (until the global timeout) for all instances to report healthy before mocing forward with scaling down the outgoing instance set. This feature can be disabled with the `--skip-health-check` flag.