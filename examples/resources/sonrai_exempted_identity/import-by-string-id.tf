# Import ID: identity@scope, with @sso appended for an SSO actor id.
import {
  to = sonrai_exempted_identity.break_glass
  id = "arn:aws:iam::*:role/BreakGlass@aws/r-abcd"
}

import {
  to = sonrai_exempted_identity.sso_operator
  id = "breakglass@example.com@aws/r-abcd@sso"
}
