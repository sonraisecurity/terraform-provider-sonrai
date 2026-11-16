# Import ID: identity@scope, with @sso appended for an SSO actor id.
terraform import sonrai_exempted_identity.break_glass 'arn:aws:iam::*:role/BreakGlass@aws/r-abcd'
terraform import sonrai_exempted_identity.sso_operator 'breakglass@example.com@aws/r-abcd@sso'
