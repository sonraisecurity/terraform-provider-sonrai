# Import ID: the scope path, or root_scope@resource_id for a resource that
# names its scope by id.
terraform import sonrai_jit_configuration.org 'aws/r-abcd'
terraform import sonrai_jit_configuration.sandbox 'aws/r-abcd@123456789012'
