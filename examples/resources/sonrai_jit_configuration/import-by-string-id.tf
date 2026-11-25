# Import ID: root_scope@resource_id, or the full scope path for a resource
# that sets scope. The resource is authoritative for that scope, so list every
# entitlement already enrolled there or the next apply unenrols it.
import {
  to = sonrai_jit_configuration.sandbox
  id = "aws/r-abcd@123456789012"
}
