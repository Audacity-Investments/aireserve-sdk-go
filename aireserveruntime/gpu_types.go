package aireserveruntime

// GPU infrastructure types (spec §10). Field names mirror the wire objects;
// prices are six-decimal USD strings, never floats. No type carries a
// provider, data-center or tenancy identifier.

// GpuCapabilities says what a quote, instance or cluster can do. They are the
// only provider-derived facts an SDK sees; consult them rather than assume.
type GpuCapabilities struct {
	Start                 bool `json:"start"`
	Stop                  bool `json:"stop"`
	Restart               bool `json:"restart"`
	SSH                   bool `json:"ssh"`
	Logs                  bool `json:"logs"`
	PersistentVolumes     bool `json:"persistentVolumes"`
	StopPreservesRootDisk bool `json:"stopPreservesRootDisk"`
}

// GpuPersistentStorage is a persistent volume as seen from an instance, a
// cluster or a quote, with the facts that matter for the bill.
type GpuPersistentStorage struct {
	VolumeID          *string `json:"volumeId"`
	SizeGb            int     `json:"sizeGb"`
	MountPath         string  `json:"mountPath"`
	Status            string  `json:"status,omitempty"`
	UsdPerHour        string  `json:"usdPerHour"`
	BillsUntilDeleted bool    `json:"billsUntilDeleted"`
	SurvivesStop      bool    `json:"survivesStop"`
	SurvivesTerminate bool    `json:"survivesTerminate"`
	MultiAttach       bool    `json:"multiAttach"`
}

// GpuOffer is what AI Reserve can provide for one accelerator at one count,
// folded across every backing provider.
type GpuOffer struct {
	Accelerator    string          `json:"accelerator"`
	Label          string          `json:"label"`
	MemoryGb       int             `json:"memoryGb"`
	Manufacturer   string          `json:"manufacturer"`
	Grade          string          `json:"grade"`
	Count          int             `json:"count"`
	Availability   string          `json:"availability"`
	FromUsdPerHour *string         `json:"fromUsdPerHour"`
	Regions        []string        `json:"regions"`
	Capabilities   GpuCapabilities `json:"capabilities"`
}

// ListGpuOffersInput filters GET /v1/gpu/offers. Zero values are omitted.
type ListGpuOffersInput struct {
	Count        int
	Region       string
	Accelerator  string
	DiskGb       int
	PersistentGb int
}

// GpuPort is one exposed port on an instance or every cluster node.
type GpuPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// GpuStorageRequest asks for a new persistent volume beside the GPU.
type GpuStorageRequest struct {
	PersistentGb int    `json:"persistentGb"`
	MountPath    string `json:"mountPath,omitempty"`
}

// InstanceRequest prices or launches one instance. Accelerator is a public id
// such as H100_80GB (see Gpu.Offers.List); Region a normalized region; never a
// provider or data center. For a launch, QuoteID OR Accelerator+MaxHourlyPrice.
type InstanceRequest struct {
	QuoteID                 string             `json:"quoteId,omitempty"`
	Accelerator             string             `json:"accelerator,omitempty"`
	MaxHourlyPrice          string             `json:"maxHourlyPrice,omitempty"`
	Count                   int                `json:"count,omitempty"`
	Region                  string             `json:"region,omitempty"`
	Name                    string             `json:"name,omitempty"`
	Image                   string             `json:"image,omitempty"`
	DiskGb                  int                `json:"diskGb,omitempty"`
	VolumeID                string             `json:"volumeId,omitempty"`
	VolumeMountPath         string             `json:"volumeMountPath,omitempty"`
	Storage                 *GpuStorageRequest `json:"storage,omitempty"`
	RegistryCredentialID    string             `json:"registryCredentialId,omitempty"`
	SSHKeyIDs               []string           `json:"sshKeyIds,omitempty"`
	SSHPublicKey            string             `json:"sshPublicKey,omitempty"`
	Env                     map[string]string  `json:"env,omitempty"`
	Ports                   []GpuPort          `json:"ports,omitempty"`
	DockerStartCmd          []string           `json:"dockerStartCmd,omitempty"`
	PublicPortsAcknowledged bool               `json:"publicPortsAcknowledged,omitempty"`
	// IdempotencyKey overrides the generated sdk-instance-<action>-<uuid> key (launch only).
	IdempotencyKey string `json:"-"`
}

// ClusterRequest prices or launches a multi-node cluster: Nodes identical
// machines of GpusPerNode GPUs on a private interconnect in one location, one
// shared volume mounted on every node, no stop. Clusters take no registry
// credential (the gateway answers 400); a private image needs an instance.
type ClusterRequest struct {
	QuoteID                 string             `json:"quoteId,omitempty"`
	Accelerator             string             `json:"accelerator,omitempty"`
	Nodes                   int                `json:"nodes,omitempty"`
	GpusPerNode             int                `json:"gpusPerNode,omitempty"`
	MaxHourlyPrice          string             `json:"maxHourlyPrice,omitempty"`
	Region                  string             `json:"region,omitempty"`
	Name                    string             `json:"name,omitempty"`
	Orchestrator            string             `json:"orchestrator,omitempty"`
	Image                   string             `json:"image,omitempty"`
	DiskGbPerNode           int                `json:"diskGbPerNode,omitempty"`
	VolumeID                string             `json:"volumeId,omitempty"`
	VolumeMountPath         string             `json:"volumeMountPath,omitempty"`
	Storage                 *GpuStorageRequest `json:"storage,omitempty"`
	SSHKeyIDs               []string           `json:"sshKeyIds,omitempty"`
	SSHPublicKey            string             `json:"sshPublicKey,omitempty"`
	Env                     map[string]string  `json:"env,omitempty"`
	Ports                   []GpuPort          `json:"ports,omitempty"`
	DockerStartCmd          []string           `json:"dockerStartCmd,omitempty"`
	PublicPortsAcknowledged bool               `json:"publicPortsAcknowledged,omitempty"`
	IdempotencyKey          string             `json:"-"`
}

// GpuInstanceQuote is a priced, placeable instance request.
type GpuInstanceQuote struct {
	ID             string  `json:"id"`
	QuoteID        string  `json:"quoteId"`
	Kind           string  `json:"kind"`
	ExpiresAt      string  `json:"expiresAt"`
	Accelerator    string  `json:"accelerator"`
	Count          int     `json:"count"`
	Region         *string `json:"region"`
	Availability   string  `json:"availability"`
	MaxHourlyPrice string  `json:"maxHourlyPrice,omitempty"`
	Price          struct {
		GpuUsdPerHour               *string `json:"gpuUsdPerHour"`
		RootDiskUsdPerHour          *string `json:"rootDiskUsdPerHour"`
		InstanceUsdPerHour          string  `json:"instanceUsdPerHour"`
		PersistentStorageUsdPerHour string  `json:"persistentStorageUsdPerHour"`
		TotalRunningUsdPerHour      string  `json:"totalRunningUsdPerHour"`
		StorageOnlyUsdPerHour       string  `json:"storageOnlyUsdPerHour"`
	} `json:"price"`
	Storage struct {
		RootGb          int                   `json:"rootGb"`
		RootPersistence string                `json:"rootPersistence"`
		Persistent      *GpuPersistentStorage `json:"persistent"`
	} `json:"storage"`
	Runtime      GpuRuntime      `json:"runtime"`
	Capabilities GpuCapabilities `json:"capabilities"`
}

// GpuRuntime is the provider-neutral runtime profile.
type GpuRuntime struct {
	Type    string  `json:"type"`
	Profile *string `json:"profile"`
}

// RecoveryPolicy chooses what happens when a resource stops on its own.
type RecoveryPolicy struct {
	Mode            string `json:"mode"`
	MaxAttempts     int    `json:"maxAttempts,omitempty"`
	CooldownMinutes int    `json:"cooldownMinutes,omitempty"`
}

// RecoveryState is the health-recovery state of an instance or cluster.
type RecoveryState struct {
	Policy        RecoveryPolicy `json:"policy"`
	Attempts      int            `json:"attempts"`
	MaxAttempts   *int           `json:"maxAttempts"`
	LastAttemptAt *string        `json:"lastAttemptAt"`
	LastOutcome   *string        `json:"lastOutcome"`
}

// GpuInstance is a provider-hidden GPU instance.
type GpuInstance struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	DesiredState string `json:"desiredState"`
	Accelerator  struct {
		Model string `json:"model"`
		Count int    `json:"count"`
	} `json:"accelerator"`
	Runtime  GpuRuntime `json:"runtime"`
	Location struct {
		Region *string `json:"region"`
	} `json:"location"`
	Storage struct {
		RootGb          int                   `json:"rootGb"`
		RootPersistence string                `json:"rootPersistence"`
		VolumeID        *string               `json:"volumeId"`
		Persistent      *GpuPersistentStorage `json:"persistent"`
	} `json:"storage"`
	Price struct {
		UsdPerHour            string `json:"usdPerHour"`
		StorageOnlyUsdPerHour string `json:"storageOnlyUsdPerHour"`
	} `json:"price"`
	Capabilities GpuCapabilities `json:"capabilities"`
	Recovery     RecoveryState   `json:"recovery"`
	CreatedAt    string          `json:"createdAt"`
	LastError    *string         `json:"lastError"`
}

// GpuSSH is a structured SSH endpoint.
type GpuSSH struct {
	Command string `json:"command"`
	User    string `json:"user"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

// GpuConnections is where to connect to a running instance.
type GpuConnections struct {
	Available bool    `json:"available"`
	SSH       *GpuSSH `json:"ssh"`
	HTTP      []struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	} `json:"http"`
}

// GpuClusterNode is a read-only member of a cluster.
type GpuClusterNode struct {
	Rank         *int    `json:"rank"`
	Role         *string `json:"role"`
	Status       string  `json:"status"`
	OverlayIP    *string `json:"overlayIp"`
	SSHAvailable bool    `json:"sshAvailable"`
}

// GpuCluster is a provider-hidden multi-node cluster.
type GpuCluster struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	DesiredState string `json:"desiredState"`
	Accelerator  struct {
		Model   string `json:"model"`
		Count   int    `json:"count"`
		PerNode int    `json:"perNode"`
	} `json:"accelerator"`
	Topology struct {
		Nodes        int    `json:"nodes"`
		GpusPerNode  int    `json:"gpusPerNode"`
		Orchestrator string `json:"orchestrator"`
		Interconnect string `json:"interconnect"`
	} `json:"topology"`
	Runtime  GpuRuntime `json:"runtime"`
	Location struct {
		Region *string `json:"region"`
	} `json:"location"`
	Storage struct {
		RootGbPerNode   int                   `json:"rootGbPerNode"`
		RootPersistence string                `json:"rootPersistence"`
		VolumeID        *string               `json:"volumeId"`
		Persistent      *GpuPersistentStorage `json:"persistent"`
	} `json:"storage"`
	Price struct {
		UsdPerHour            string `json:"usdPerHour"`
		StorageOnlyUsdPerHour string `json:"storageOnlyUsdPerHour"`
	} `json:"price"`
	Capabilities GpuCapabilities  `json:"capabilities"`
	Recovery     RecoveryState    `json:"recovery"`
	Nodes        []GpuClusterNode `json:"nodes"`
	CreatedAt    string           `json:"createdAt"`
	ReadyAt      *string          `json:"readyAt"`
	LastError    *string          `json:"lastError"`
}

// GpuClusterQuote is a priced, placeable cluster request.
type GpuClusterQuote struct {
	ID             string  `json:"id"`
	QuoteID        string  `json:"quoteId"`
	Kind           string  `json:"kind"`
	ExpiresAt      string  `json:"expiresAt"`
	Accelerator    string  `json:"accelerator"`
	Nodes          int     `json:"nodes"`
	GpusPerNode    int     `json:"gpusPerNode"`
	TotalGpus      int     `json:"totalGpus"`
	Orchestrator   string  `json:"orchestrator"`
	Region         *string `json:"region"`
	Availability   string  `json:"availability"`
	MaxHourlyPrice string  `json:"maxHourlyPrice,omitempty"`
	Price          struct {
		GpuUsdPerHour               string `json:"gpuUsdPerHour"`
		GpusUsdPerHour              string `json:"gpusUsdPerHour"`
		RootDiskUsdPerHour          string `json:"rootDiskUsdPerHour"`
		ClusterUsdPerHour           string `json:"clusterUsdPerHour"`
		PersistentStorageUsdPerHour string `json:"persistentStorageUsdPerHour"`
		TotalRunningUsdPerHour      string `json:"totalRunningUsdPerHour"`
		StorageOnlyUsdPerHour       string `json:"storageOnlyUsdPerHour"`
		NetworkingIncluded          bool   `json:"networkingIncluded"`
	} `json:"price"`
	Storage struct {
		RootGbPerNode   int                   `json:"rootGbPerNode"`
		RootPersistence string                `json:"rootPersistence"`
		Persistent      *GpuPersistentStorage `json:"persistent"`
	} `json:"storage"`
	Runtime      GpuRuntime      `json:"runtime"`
	Capabilities GpuCapabilities `json:"capabilities"`
}

// GpuClusterConnections is where to connect to a running cluster.
type GpuClusterConnections struct {
	Available bool `json:"available"`
	Primary   *struct {
		Rank int     `json:"rank"`
		SSH  *GpuSSH `json:"ssh"`
	} `json:"primary"`
	Nodes []struct {
		Rank      *int    `json:"rank"`
		Role      *string `json:"role"`
		OverlayIP *string `json:"overlayIp"`
		SSH       *GpuSSH `json:"ssh"`
	} `json:"nodes"`
	Network *struct {
		CIDR string `json:"cidr"`
	} `json:"network"`
}

// GpuVolume is a persistent volume. It bills hourly until deleted.
type GpuVolume struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Status             string  `json:"status"`
	Region             *string `json:"region"`
	SizeGb             int     `json:"sizeGb"`
	AttachedInstanceID *string `json:"attachedInstanceId"`
	AttachedClusterID  *string `json:"attachedClusterId"`
	HourlyRateUsd      string  `json:"hourlyRateUsd"`
	PaidThroughAt      *string `json:"paidThroughAt"`
	CreatedAt          string  `json:"createdAt"`
}

// CreateVolumeInput creates a standalone volume in a region.
type CreateVolumeInput struct {
	Name            string `json:"name"`
	Region          string `json:"region"`
	SizeGb          int    `json:"sizeGb"`
	AcceleratorHint string `json:"acceleratorHint,omitempty"`
	// AcknowledgeHourlyBilling must be true: the volume bills from creation until deleted.
	AcknowledgeHourlyBilling bool   `json:"-"`
	IdempotencyKey           string `json:"-"`
}

// GrowVolumeInput grows a volume; the server prices the new size where the
// volume lives. MaxHourlyPrice caps the new storage rate.
type GrowVolumeInput struct {
	SizeGb         int    `json:"sizeGb"`
	MaxHourlyPrice string `json:"maxHourlyPrice,omitempty"`
	IdempotencyKey string `json:"-"`
}

// GpuSSHKey is a registered SSH public key.
type GpuSSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	State       string `json:"state"`
	CreatedAt   string `json:"createdAt"`
}

// GpuRegistryCredential is registry credential metadata; the secret is never returned.
type GpuRegistryCredential struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RegistryURL string `json:"registryUrl"`
	Username    string `json:"username"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
}

// AddRegistryInput stores a credential for a private image registry.
type AddRegistryInput struct {
	Name           string `json:"name"`
	RegistryURL    string `json:"registryUrl"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	IdempotencyKey string `json:"-"`
}

// GpuTemplate is a curated or organization-owned launch template.
type GpuTemplate struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	Curated         bool      `json:"curated"`
	ImageName       string    `json:"imageName"`
	ContainerDiskGb *int      `json:"containerDiskGb"`
	Ports           []GpuPort `json:"ports"`
	EnvKeys         []string  `json:"envKeys"`
	IsActive        bool      `json:"isActive"`
}

// TemplateInput creates (all fields) or updates (any subset) a private template.
type TemplateInput struct {
	Name            string            `json:"name,omitempty"`
	ImageName       string            `json:"imageName,omitempty"`
	ContainerDiskGb int               `json:"containerDiskGb,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	Ports           []GpuPort         `json:"ports,omitempty"`
	DockerStartCmd  []string          `json:"dockerStartCmd,omitempty"`
	IsActive        *bool             `json:"isActive,omitempty"`
	IdempotencyKey  string            `json:"-"`
}

// GpuOperationRecord is an asynchronous compute operation.
type GpuOperationRecord struct {
	ID           string               `json:"id"`
	OperationID  string               `json:"operationId"`
	ResourceID   string               `json:"resourceId"`
	ResourceType string               `json:"resourceType"`
	Action       string               `json:"action"`
	Status       string               `json:"status"`
	ErrorMessage *string              `json:"errorMessage"`
	Failure      *GpuOperationFailure `json:"failure"`
	CompletedAt  *string              `json:"completedAt"`
}

// GpuOperationFailure mirrors types.OperationFailure on the wire record.
type GpuOperationFailure struct {
	Code    string `json:"code"`
	Actor   string `json:"actor"`
	Message string `json:"message"`
	Next    string `json:"next"`
}

// GpuUsageCharge is one immutable billing window.
type GpuUsageCharge struct {
	ID            string  `json:"id"`
	ResourceType  string  `json:"resourceType"`
	ResourceID    string  `json:"resourceId"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	AmountUsd     string  `json:"amountUsd"`
	IntervalStart *string `json:"intervalStart"`
	IntervalEnd   *string `json:"intervalEnd"`
}

// GpuUsagePage is one page of charges.
type GpuUsagePage struct {
	Charges    []GpuUsageCharge
	NextCursor string
}

// GpuLogEvent is one relayed log line.
type GpuLogEvent struct {
	Timestamp string
	Source    string
	Line      string
	EventID   string
}

// GpuLogOptions narrows a log replay. Source is "container" or "system";
// Since an RFC 3339 timestamp; Node the cluster rank (clusters only).
type GpuLogOptions struct {
	Tail   int
	Source string
	Since  string
	Node   int
}

// WaitOptions bounds Wait. Zero values take the per-resource defaults.
type WaitOptions struct {
	Timeout      int64 // milliseconds
	PollInterval int64 // milliseconds
}
