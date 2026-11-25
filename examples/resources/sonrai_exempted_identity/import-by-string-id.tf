# Import ID: identity@root_scope@resource_id, with @sso appended for an SSO
# actor id.
import {
  to = sonrai_exempted_identity.break_glass
  id = "*e/BreakGlass@aws/r-abcd@r-abcd"
}

import {
  to = sonrai_exempted_identity.sso_operator
  id = "operator@example.com@aws/r-abcd@r-abcd@sso"
}
