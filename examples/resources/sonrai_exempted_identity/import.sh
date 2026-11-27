# Import ID: identity@root_scope@resource_id, with @sso appended for an SSO
# actor id.
terraform import sonrai_exempted_identity.break_glass '*e/BreakGlass@aws/r-abcd@r-abcd'
terraform import sonrai_exempted_identity.sso_operator 'operator@example.com@aws/r-abcd@r-abcd@sso'
