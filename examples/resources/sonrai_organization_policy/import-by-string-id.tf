# Import ID: the policy's id. Find it with the sonrai_organization_policies
# data source. Only a policy written in Sonrai can be imported, and content is
# the whole document, so write all of it or the next apply replaces the rest.
import {
  to = sonrai_organization_policy.deny_leaving_org
  id = "a1b2c3d4-0000-4000-8000-000000000000"
}
