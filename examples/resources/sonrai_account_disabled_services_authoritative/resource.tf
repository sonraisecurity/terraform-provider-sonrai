# Declares the complete set of disabled services for one account. Services
# disabled in the account but not listed here are re-enabled.
#
# Import the account first to see what it already has:
#   terraform import sonrai_account_disabled_services_authoritative.prod \
#     'aws/r-abcd@123456789012'
resource "sonrai_account_disabled_services_authoritative" "prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"

  control_keys = [
    "cloud9",
    "sagemaker",
  ]
}

output "prod_scope" {
  value = sonrai_account_disabled_services_authoritative.prod.scope
}
