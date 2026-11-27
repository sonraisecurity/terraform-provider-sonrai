# Attach at one level of a branch. The policy applies at the target and
# everything beneath it, including accounts added later.
resource "sonrai_organization_policy_attachment" "workloads" {
  policy_id   = sonrai_organization_policy.deny_leaving_org.id
  root_scope  = "aws/r-abcd"
  resource_id = "ou-abcd-1a2b3c4d"
}

# One account elsewhere in the organization.
resource "sonrai_organization_policy_attachment" "sandbox" {
  policy_id   = sonrai_organization_policy.deny_leaving_org.id
  root_scope  = "aws/r-abcd"
  resource_id = "123456789012"
}

# Attaching and detaching are staged. Both take effect when submitted.
resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    workloads = sonrai_organization_policy_attachment.workloads.revision
    sandbox   = sonrai_organization_policy_attachment.sandbox.revision
    policy    = sonrai_organization_policy.deny_leaving_org.revision
  }
}
