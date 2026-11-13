# Submit staged changes and deploy the resulting template. Once this has been
# applied, leave it true: setting it back to false sets count = 0 on the
# submission and the CloudFormation stack, which plans to delete the stack and
# with it the firewall's IAM footprint, without unapplying anything. To take
# the firewall down properly, use reset below.
submit = true

# Unapply every control under root_scope and tear the deployment down. One
# apply deletes the stack, runs the reset and waits for it to finish. This
# clears the whole root scope, not only what this configuration manages, so it
# takes both variables: reset_confirm must repeat root_scope exactly.
#
# The stack's prevent_destroy lifecycle block in main.tf has to come out first,
# and should go back afterwards. See the README.
reset         = false
reset_confirm = ""

# Accounts are addressed by their cloud-native id, wherever they sit in the
# hierarchy. 123456789012 is directly under the root here and 210987654321 is
# several OUs deep; both resolve the same way.
disabled_services = {
  cloud9_123456789012     = { resource_id = "123456789012", control_key = "cloud9" }
  gamelift_123456789012   = { resource_id = "123456789012", control_key = "gameliftstreams" }
  healthlake_210987654321 = { resource_id = "210987654321", control_key = "healthlake" }
}

# Exemptions from whole classes of control, rather than from one service.
# Identities are matched as patterns, so one entry covers every account under
# the scope.
exempted_identities = {
  # A break-glass role in every account, exempt from protects and blocks.
  break_glass = {
    identity    = "arn:aws:iam::*:role/BreakGlass"
    exempt_from = ["service_protect", "service_block"]
  }

  # An IAM Identity Center user rather than an ARN pattern.
  sso_operator = {
    identity    = "breakglass@example.com"
    sso         = true
    exempt_from = ["service_block"]
  }

  # Scoped to one account. scope defaults to root_scope when omitted, as above.
  deploy_robot = {
    identity    = "arn:aws:iam::123456789012:role/DeployRobot"
    exempt_from = ["service_protect"]
    scope       = "aws/r-abcd/123456789012"
  }
}

# Protects firewall a service's sensitive permissions for every identity except
# those exempted here. Read the platform's suggestions with the
# sonrai_suggested_exemptions data source before filling these in.
protected_services = {
  # Both kinds of exemption: ARNs and an SSO actor id.
  iam_123456789012 = {
    resource_id = "123456789012"
    control_key = "iam"
    exempt_identities = [
      "arn:aws:iam::123456789012:role/CrossAccountIAMAdminRole",
      "arn:aws:iam::123456789012:role/CrossAccountAccessAnalyzerAdminRole",
    ]
    exempt_sso_actor_ids = ["breakglass@example.com"]
  }

  lambda_123456789012 = {
    resource_id       = "123456789012"
    control_key       = "lambda"
    exempt_identities = ["arn:aws:iam::123456789012:role/CrossAccountLambdaAdminRole"]
  }

  # A protect with no exemptions at all.
  ec2_210987654321 = {
    resource_id = "210987654321"
    control_key = "ec2"
  }
}
