# Every organization policy Sonrai knows of in the AWS organization: the ones
# written in Sonrai, and the SCPs and RCPs it discovered in AWS.
data "sonrai_organization_policies" "all" {
  root_scope = "aws/r-abcd"
}

# The ones Sonrai deploys, with where each is attached.
output "managed_policies" {
  value = {
    for p in data.sonrai_organization_policies.all.policies : p.name => {
      id          = p.id
      policy_type = p.policy_type
      # Ids to give sonrai_organization_policy_attachment as resource_id.
      attached_to = p.attached_resource_ids
    }
    if p.managed
  }
}

# Policies still attached by AWS itself, which Sonrai only reports on.
output "unmanaged_policies" {
  value = [for p in data.sonrai_organization_policies.all.policies : p.name if !p.managed]
}
