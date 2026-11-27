variable "root_scope" {
  description = "Cloud organization root scope these controls belong to."
  type        = string
  default     = "aws/r-abcd"
}

variable "disabled_services" {
  description = "Services to disable: cloud-native account, project or subscription id => control keys to disable in it."
  type        = map(set(string))
  default     = {}

  validation {
    condition = alltrue([
      for account_id, control_keys in var.disabled_services :
      length(trimspace(account_id)) > 0 && length(control_keys) > 0 &&
      alltrue([for k in control_keys : can(regex("^[a-z0-9][a-z0-9_-]*$", k))])
    ])
    error_message = "Each disabled_services entry needs a non-empty account id and at least one lowercase control key, such as \"cloud9\"."
  }
}

variable "protected_services" {
  description = "Services to protect, keyed by a name of your choosing."
  type = map(object({
    resource_id          = string
    control_key          = string
    exempt_identities    = optional(set(string), [])
    exempt_sso_actor_ids = optional(set(string), [])
  }))
  default = {}
}

variable "exempted_identities" {
  description = "Identities exempt from whole control classes, keyed by a name of your choosing. resource_id is the account or organizational unit the exemption is limited to; leave it out to exempt across the whole organization."
  type = map(object({
    identity    = string
    exempt_from = set(string)
    sso         = optional(bool, false)
    resource_id = optional(string)
    scope       = optional(string)
  }))
  default = {}

  # scope is declared only so that it can be refused. Terraform drops an
  # attribute an object type does not name, and an entry that lost its scope
  # that way would quietly become an exemption across the whole organization.
  validation {
    condition     = alltrue([for e in values(var.exempted_identities) : e.scope == null])
    error_message = "exempted_identities entries no longer take scope. Give resource_id, the id of the account or organizational unit, instead."
  }

  validation {
    condition = alltrue([
      for e in values(var.exempted_identities) :
      e.resource_id == null || can(regex("^([0-9]{12}|ou-[a-z0-9]+-[a-z0-9]+|r-[a-z0-9]+)$", e.resource_id))
    ])
    error_message = "resource_id must be an account id such as \"123456789012\", an organizational unit id such as \"ou-ab12-cdef3456\" or a root id such as \"r-abcd\"."
  }
}

variable "region_controls" {
  description = "Regions to control: cloud-native account id => region id => \"disabled\", or \"enabled\" to override an ancestor that disabled it."
  type        = map(map(string))
  default     = {}

  validation {
    condition = alltrue(flatten([
      for account_id, regions in var.region_controls : [
        for region_id, state in regions :
        can(regex("^[0-9]{12}$", account_id)) &&
        can(regex("^[a-z]{2}(-[a-z]+)+-[0-9]+$", region_id)) &&
        contains(["disabled", "enabled"], state)
      ]
    ]))
    error_message = "Each region_controls entry needs a 12-digit AWS account id, a region id such as \"ap-south-1\", and a state of \"disabled\" or \"enabled\"."
  }
}

variable "jit_configurations" {
  description = "Just-in-Time configuration, keyed by the cloud-native id of the account, organizational unit or root it applies to. Each entry replaces whatever is configured there already."
  type = map(object({
    deny_first = optional(bool, false)
    enrolled_entitlements = optional(set(object({
      name     = string
      type     = optional(string, "AWS_PERMISSION_SET")
      auto_pod = optional(bool, false)
    })), [])
  }))
  default = {}

  validation {
    condition = alltrue(flatten([
      for target, c in var.jit_configurations : concat(
        [can(regex("^([0-9]{12}|ou-[a-z0-9]+-[a-z0-9]+|r-[a-z0-9]+)$", target))],
        [for e in c.enrolled_entitlements : contains(["AWS_PERMISSION_SET", "AWS_ACCOUNT_ACCESS_ROLE", "AWS_SAML_ROLE"], e.type)]
      )
    ]))
    error_message = "Each jit_configurations key must be an account id such as \"123456789012\", an organizational unit id such as \"ou-ab12-cdef3456\" or a root id such as \"r-abcd\", and each entitlement type one of AWS_PERMISSION_SET, AWS_ACCOUNT_ACCESS_ROLE or AWS_SAML_ROLE."
  }
}

variable "custom_controls" {
  description = "Custom controls, keyed by a name of your choosing. Each is a named group of permissions that can then be protected or disabled like a service: use the name given here as a control key in protected_services or disabled_services."
  type = map(object({
    title       = string
    description = string
    permissions = set(string)
    cloud_type  = optional(string, "aws")
  }))
  default = {}

  validation {
    condition = alltrue([
      for c in values(var.custom_controls) :
      contains(["aws", "gcp"], c.cloud_type) && length(c.permissions) > 0 &&
      can(regex("^[A-Za-z0-9][A-Za-z0-9 _.-]*$", c.title))
    ])
    error_message = "Each custom control needs at least one permission, a cloud_type of aws or gcp, and a title of letters, numbers, spaces, hyphens, underscores and periods."
  }
}

variable "organization_policies" {
  description = "Organization policies of your own, keyed by a name of your choosing, which is the policy's name unless name is given. content is the policy document as JSON; attach_to lists the cloud-native ids of the accounts, organizational units or root to attach it to. AWS only."
  type = map(object({
    content     = string
    policy_type = optional(string, "SERVICE_CONTROL_POLICY")
    name        = optional(string)
    description = optional(string)
    attach_to   = optional(set(string), [])
  }))
  default = {}

  validation {
    condition = alltrue(flatten([
      for key, p in var.organization_policies : concat(
        [contains(["SERVICE_CONTROL_POLICY", "RESOURCE_CONTROL_POLICY"], p.policy_type)],
        [can(jsondecode(p.content).Statement)],
        [!startswith(coalesce(p.name, key), "Sonrai")],
        [for target in p.attach_to : can(regex("^([0-9]{12}|ou-[a-z0-9]+-[a-z0-9]+|r-[a-z0-9]+)$", target))]
      )
    ]))
    error_message = "Each organization policy needs a JSON policy document with a Statement, a policy_type of SERVICE_CONTROL_POLICY or RESOURCE_CONTROL_POLICY, a name that does not start with Sonrai, and attach_to ids that are a 12-digit account id, an ou-... id or an r-... id."
  }
}

variable "approvers" {
  description = "Who approves access requests, keyed by the cloud-native id of the account, organizational unit or root. Each entry replaces the approvers assigned there, and takes effect when applied rather than when submitted."
  type = map(object({
    approvers     = set(string)
    approval_type = string
  }))
  default = {}

  validation {
    condition = alltrue(flatten([
      for target, a in var.approvers : concat(
        [can(regex("^([0-9]{12}|ou-[a-z0-9]+-[a-z0-9]+|r-[a-z0-9]+)$", target))],
        [contains(["exclude_self", "assigned_approvers", "everyone_at_scope"], a.approval_type)],
        [length(a.approvers) <= 100],
        [for email in a.approvers : email == lower(email)]
      )
    ]))
    error_message = "Each approvers key must be an account, organizational unit or root id; approval_type one of exclude_self, assigned_approvers or everyone_at_scope; and approvers at most 100 email addresses in lower case."
  }
}

variable "submit" {
  description = "Submit staged changes and deploy the resulting template."
  type        = bool
  default     = false
}

variable "reset" {
  description = "Tear the deployment down and return every control under root_scope to staged. Organization-wide and destructive."
  type        = bool
  default     = false
}

variable "reset_confirm" {
  description = "Repeat root_scope here to arm the reset. It is a second variable rather than root_scope itself so that setting reset alone cannot clear an organization; the provider rejects the plan unless the two match."
  type        = string
  default     = ""
}

variable "aws_region" {
  description = "Region the CloudFormation deploy runs in."
  type        = string
  default     = "us-east-1"
}

variable "stack_name" {
  description = "Name of the firewall's CloudFormation stack."
  type        = string
  default     = "SonraiCloudPermissionFirewall"
}
