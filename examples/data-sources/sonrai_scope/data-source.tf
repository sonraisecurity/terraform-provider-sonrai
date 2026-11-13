# Resolves a scope path from a cloud-native id.
data "sonrai_scope" "account" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
}

data "sonrai_scope" "org_unit" {
  root_scope  = "aws/r-abcd"
  resource_id = "ou-abcd-1a2b3c"
}

# Bare ids resolve the same as qualified ones.
data "sonrai_scope" "gcp_project" {
  root_scope  = "gcp/organizations|1234567890"
  resource_id = "my-project"
}

output "scopes" {
  value = {
    account     = data.sonrai_scope.account.scope
    org_unit    = data.sonrai_scope.org_unit.scope
    gcp_project = data.sonrai_scope.gcp_project.scope
  }
}
