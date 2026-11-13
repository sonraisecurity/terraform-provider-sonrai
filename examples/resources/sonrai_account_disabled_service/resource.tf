# Disable a service in one account. Claims only this service; anything else
# disabled in the account is left alone.
resource "sonrai_account_disabled_service" "cloud9_prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  control_key = "cloud9"
}

# To cover several accounts, list each one.
resource "sonrai_account_disabled_service" "cloud9_sandbox" {
  root_scope  = "aws/r-abcd"
  resource_id = "210987654321"
  control_key = "cloud9"
}

output "cloud9_prod" {
  value = {
    scope   = sonrai_account_disabled_service.cloud9_prod.scope
    status  = sonrai_account_disabled_service.cloud9_prod.status
    pending = sonrai_account_disabled_service.cloud9_prod.pending
  }
}
