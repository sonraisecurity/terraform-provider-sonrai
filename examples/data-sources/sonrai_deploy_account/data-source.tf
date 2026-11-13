# Resolves the account the CloudFormation template deploys into.
data "sonrai_deploy_account" "firewall" {
  root_scope = "aws/r-abcd"
}

output "deploy_target" {
  value = {
    account_id = data.sonrai_deploy_account.firewall.account_id
    delegated  = data.sonrai_deploy_account.firewall.delegated
  }
}

# Point AWS credentials at that account.
provider "aws" {
  region = "us-east-1"

  assume_role {
    role_arn = "arn:aws:iam::${data.sonrai_deploy_account.firewall.account_id}:role/YourDeployRole"
  }
}
