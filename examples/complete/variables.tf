variable "root_scope" {
  description = "Cloud organization root scope these controls belong to."
  type        = string
  default     = "aws/r-abcd"
}

variable "disabled_services" {
  description = "Services to disable, keyed by a name of your choosing."
  type = map(object({
    resource_id = string
    control_key = string
  }))
  default = {}
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
  description = "Identities exempt from whole control classes, keyed by a name of your choosing."
  type = map(object({
    identity    = string
    exempt_from = set(string)
    sso         = optional(bool, false)
    scope       = optional(string)
  }))
  default = {}
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
