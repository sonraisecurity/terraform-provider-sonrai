# Disable a region in one account.
resource "sonrai_region_control" "no_apac_sandbox" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
  region_id   = "ap-south-1"
  state       = "disabled"
}

# Org-wide posture is per-account on purpose: a new account added to the list
# shows up as a planned change, where a container-level control would expand
# once and silently skip accounts added later.
variable "account_ids" {
  type = set(string)
}

resource "sonrai_region_control" "no_apac" {
  for_each    = var.account_ids
  root_scope  = "aws/r-abcd"
  resource_id = each.value
  region_id   = "ap-south-1"
  state       = "disabled"
}
