# A break-glass role exempt across the whole organization. The identity is
# matched as a pattern, so one entry covers the role in every account,
# including accounts added later. Prefer a short pattern: it is repeated across
# the size-limited policies the platform deploys, so *e/BreakGlass leaves far
# more room than arn:aws:iam::*:role/BreakGlass, which names the same role.
resource "sonrai_exempted_identity" "break_glass" {
  scope       = "aws/r-abcd"
  identity    = "*e/BreakGlass"
  exempt_from = ["service_protect", "service_block"]
}

# An IAM Identity Center user rather than an ARN pattern.
resource "sonrai_exempted_identity" "sso_operator" {
  scope       = "aws/r-abcd"
  identity    = "operator@example.com"
  sso         = true
  exempt_from = ["service_block"]
}

# Scoped to a single account instead of the whole organization. A full ARN
# matches only this identity in this account.
resource "sonrai_exempted_identity" "deploy_robot" {
  scope       = "aws/r-abcd/123456789012"
  identity    = "arn:aws:iam::123456789012:role/DeployRobot"
  exempt_from = ["service_protect"]
}
