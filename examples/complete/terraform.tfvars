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
#
# Each service listed is managed on its own. A service already disabled in the
# account, from the console or elsewhere, is rejected rather than taken over,
# and services not listed here are left alone, never re-enabled.
disabled_services = {
  "123456789012" = ["cloud9", "gameliftstreams"]
  "210987654321" = ["healthlake"]
}

# Regions, per account: "disabled" stops the firewall operating there, and
# "enabled" overrides an ancestor that disabled it. Critical regions cannot be
# disabled, and destroying an entry restores the state the account had before.
region_controls = {
  "123456789012" = { "ap-south-1" = "disabled", "ap-southeast-3" = "disabled" }
}

# Just-in-Time access, keyed by id like everything above. An organizational
# unit id such as ou-ab12-cdef3456 or the root id, r-abcd, works here too, and
# covers everything beneath it. Each entry is authoritative: it replaces
# whatever is configured there already, so anything enrolled in the console and
# not listed here is unenrolled on apply.
jit_configurations = {
  "123456789012" = {
    deny_first = true
    enrolled_entitlements = [
      { name = "PowerUserAccess" },
      { name = "BreakGlassAccess", type = "AWS_ACCOUNT_ACCESS_ROLE", auto_pod = true },
    ]
  }
}

# Custom controls: named groups of permissions to protect or disable, beyond
# the services the firewall ships with. They belong to the whole organization
# and are defined on apply, whether or not submit is set, but protect nothing
# until protected_services or disabled_services names one, as data_destruction
# is named below.
#
# A name already taken by a control made elsewhere is rejected until it is
# imported. Editing the permissions of a control that is deployed redeploys it
# in the same apply. To remove one that is attached, take it out of
# protected_services and disabled_services and apply, then remove it here and
# apply again.
custom_controls = {
  data_destruction = {
    title       = "Data Destruction"
    description = "Deleting buckets, tables and snapshots"
    permissions = ["s3:DeleteBucket", "dynamodb:DeleteTable", "ec2:DeleteSnapshot"]
  }
}

# Organization policies of your own: service control or resource control
# policies the firewall deploys alongside its own, keyed by name. AWS only.
# Defining one takes effect on apply and is in force nowhere; attach_to names
# the accounts, organizational units or root it applies to, and that part is
# staged. Attach at one level of a branch: an account under an attached
# organizational unit already has the policy.
#
# A name a managed policy already has is rejected until it is imported.
# Editing a deployed policy redeploys it in the same apply. To remove one that
# is attached, empty its attach_to and apply, then remove it here and apply
# again.
organization_policies = {
  deny-leaving-organization = {
    description = "Accounts cannot remove themselves from the organization"
    attach_to   = ["ou-abcd-1a2b3c4d"]
    content     = <<-JSON
      {
        "Version": "2012-10-17",
        "Statement": [{
          "Sid": "DenyLeaveOrganization",
          "Effect": "Deny",
          "Action": ["organizations:LeaveOrganization"],
          "Resource": "*"
        }]
      }
    JSON
  }
}

# Who approves access requests, keyed by account, organizational unit or root
# id. Unlike everything else in this file, approvers are not staged: they take
# effect on apply, whether or not submit is set.
#
# Each entry is authoritative for the approvers assigned there, and a scope
# that already has approvers is rejected until it is imported. An address that
# is not yet a Sonrai user is sent an invite. Writing approvers removes any
# approver exceptions set on that scope in the console.
approvers = {
  # One account: these two approve, but never their own request.
  "123456789012" = {
    approvers     = ["cloud-approvers@example.com", "amy@example.com"]
    approval_type = "exclude_self"
  }

  # An organizational unit where anyone may approve, so it names nobody.
  "ou-ab12-sandbox1" = {
    approvers     = []
    approval_type = "everyone_at_scope"
  }
}

# Exemptions from whole classes of control, rather than from one service.
# Identities are matched as patterns, so one entry covers every account under
# the scope. Prefer short patterns, such as *e/BreakGlass rather than
# arn:aws:iam::*:role/BreakGlass: each one is repeated across the size-limited
# policies the platform deploys. protected_services below needs full ARNs.
exempted_identities = {
  # A break-glass role in every account, exempt from protects and blocks.
  break_glass = {
    identity    = "*e/BreakGlass"
    exempt_from = ["service_protect", "service_block"]
  }

  # An IAM Identity Center user rather than an ARN pattern.
  sso_operator = {
    identity    = "breakglass@example.com"
    sso         = true
    exempt_from = ["service_block"]
  }

  # Limited to one account. resource_id does the limiting, so the pattern
  # stays short. It takes an organizational unit id the same way, and without
  # it the exemption covers the whole organization, as above.
  deploy_robot = {
    identity    = "*e/DeployRobot"
    exempt_from = ["service_protect"]
    resource_id = "123456789012"
  }
}

# Protects firewall a service's sensitive permissions for every identity except
# those exempted here. Read the platform's suggestions with the
# sonrai_suggested_exemptions data source before filling these in.
# exempt_identities must be full ARNs, as the suggestions are; patterns are not
# supported. Put a role that should be exempt everywhere in
# exempted_identities instead.
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

  # A custom control, by the name it has in custom_controls above.
  data_destruction_123456789012 = {
    resource_id = "123456789012"
    control_key = "data_destruction"
  }
}
