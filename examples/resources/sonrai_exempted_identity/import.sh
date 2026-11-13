# Import by identity@scope. Append @sso when the identity is an SSO actor id.
terraform import sonrai_exempted_identity.break_glass 'arn:aws:iam::*:role/BreakGlass@aws/r-abcd'
terraform import sonrai_exempted_identity.sso_admin 'someone@example.com@aws/r-abcd@sso'
