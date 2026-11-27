# Import ID: root_scope@resource_id. The resource is authoritative for the
# approvers assigned there, so list every one of them or the next apply removes
# the rest.
import {
  to = sonrai_approvers.prod
  id = "aws/r-abcd@123456789012"
}
