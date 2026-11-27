# A service control policy of your own. It is defined as soon as this is
# applied, and is in force nowhere until it is attached.
resource "sonrai_organization_policy" "deny_leaving_org" {
  root_scope  = "aws/r-abcd"
  name        = "deny-leaving-organization"
  policy_type = "SERVICE_CONTROL_POLICY"
  description = "Accounts cannot remove themselves from the organization"

  # The whole document. The plan has the platform check it.
  content = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "DenyLeaveOrganization"
      Effect   = "Deny"
      Action   = ["organizations:LeaveOrganization"]
      Resource = "*"
    }]
  })
}

# Attaching is staged, and takes effect when submitted and deployed. The
# policy then applies to the organizational unit and everything beneath it.
resource "sonrai_organization_policy_attachment" "workloads" {
  policy_id   = sonrai_organization_policy.deny_leaving_org.id
  root_scope  = "aws/r-abcd"
  resource_id = "ou-abcd-1a2b3c4d"
}

resource "sonrai_control_submission" "firewall" {
  root_scope = "aws/r-abcd"

  triggers = {
    attachment = sonrai_organization_policy_attachment.workloads.revision
    # Moves when the deployed policy is edited, which is what redeploys it.
    # Without it the old document stays in force.
    policy = sonrai_organization_policy.deny_leaving_org.revision
  }
}
