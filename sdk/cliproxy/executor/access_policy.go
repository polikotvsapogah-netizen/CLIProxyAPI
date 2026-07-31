package executor

const (
	// ClientIDMetadataKey identifies the authenticated downstream client.
	ClientIDMetadataKey = "client_id"
	// ClientLabelMetadataKey stores the human-readable downstream client label.
	ClientLabelMetadataKey = "client_label"
	// AllowedAuthPoolsMetadataKey maps allowed auth IDs to the pool that granted access.
	AllowedAuthPoolsMetadataKey = "allowed_auth_pools"
	// SelectedPoolMetadataKey stores the selected account pool ID for this request.
	SelectedPoolMetadataKey = "selected_pool_id"
)
