# Approvers for one account. Takes effect when applied: nothing is staged, and
# no sonrai_control_submission is involved.
resource "sonrai_approvers" "prod" {
  scope = "aws/r-abcd/ou-ab12-cdef3456/123456789012"

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
resource "sonrai_approvers" "sandbox" {
  scope         = "aws/r-abcd/ou-ab12-sandbox1"
  approvers     = []
  approval_type = "everyone_at_scope"
}
