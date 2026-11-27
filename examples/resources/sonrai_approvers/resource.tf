# Approvers for one account. Takes effect when applied: nothing is staged, and
# no sonrai_control_submission is involved.
resource "sonrai_approvers" "prod" {
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"

  # Users or groups, by email address, in lower case. An address that is not
  # yet a Sonrai user is sent an invite.
  approvers = [
    "cloud-approvers@example.com",
    "amy@example.com",
  ]

  # exclude_self, assigned_approvers or everyone_at_scope.
  approval_type = "exclude_self"
}

# Anyone may approve at the sandbox OU, so it needs no approvers of its own.
# resource_id takes an organizational unit or the root as well as an account.
resource "sonrai_approvers" "sandbox" {
  root_scope    = "aws/r-abcd"
  resource_id   = "ou-ab12-sandbox1"
  approvers     = []
  approval_type = "everyone_at_scope"
}
