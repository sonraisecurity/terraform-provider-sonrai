# Complete example

One root module covering the whole cycle: disable services, protect services
with exemptions, exempt identities from whole control classes, disable regions,
configure Just-in-Time access, define custom controls, assign approvers,
submit, and deploy the resulting CloudFormation template.

Accounts, organizational units and the root are named by their cloud-native id
throughout; nothing needs a full scope path. Approvers are the one thing here
that is not staged: they take effect on apply, whether or not `submit` is set.

Everything is driven from `terraform.tfvars`. Replace the account ids, root
scope and identities with your own.

```sh
export SONRAI_ORG=your-org
export SONRAI_TOKEN=...

terraform init
terraform plan
terraform apply   # with credentials for the deploy account
```

`terraform apply` needs AWS credentials for the account the firewall deploys
into, because it creates the CloudFormation stack in the same run. Find that
account with the `sonrai_deploy_account` data source.

To take the whole thing back down, set `reset = true` and `reset_confirm` to
your root scope, remove the `prevent_destroy` block from the stack in
`main.tf`, and apply. One apply deletes the stack, resets every control under
the root scope back to staged, and waits for it to finish.

See the [walkthrough](../../GUIDE.md#walkthrough) for what each step does,
[Reset everything at once](../../GUIDE.md#reset-everything-at-once) for the
teardown in full, and
[Importing an existing deployment](../../GUIDE.md#importing-an-existing-deployment)
if the firewall is already configured.
