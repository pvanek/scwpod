// Package core is the UI-agnostic layer of scwpod. It wraps the Scaleway SDK
// and exposes plain domain types, so the same package can back the terminal
// UI today and a gomobile facade later. Keep SDK types out of its API.
package core

import "time"

// AllProjects is the scope value meaning "every project in the organization".
const AllProjects = ""

type Project struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
}

type Container struct {
	ID          string
	Name        string
	ProjectID   string
	Namespace   string
	Region      string
	Status      string
	Image       string
	Endpoint    string
	MinScale    uint32
	MaxScale    uint32
	MemoryMB    uint64
	MilliVCPU   uint32
	ErrorReason string

	Description     string
	Privacy         string
	Protocol        string
	Port            uint32
	Timeout         time.Duration
	PrivateEndpoint string
	Tags            []string
	Env             map[string]string // plain (non-secret) environment variables
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type Database struct {
	ID        string
	Name      string
	ProjectID string
	Region    string
	Engine    string
	NodeType  string
	Status    string
	HA        bool
	VolumeGB  uint64
	Endpoint  string

	VolumeType     string
	Endpoints      []string
	Encrypted      bool
	ReadReplicas   int
	BackupEnabled  bool
	BackupEveryH   uint32
	BackupKeepDays uint32
	NextBackupAt   time.Time
	Maintenances   []string
	Upgrades       []string
	Tags           []string
	CreatedAt      time.Time
}

type User struct {
	ID          string
	Email       string
	Username    string
	Type        string
	Status      string
	MFA         bool
	LastLoginAt time.Time

	FirstName string
	LastName  string
	Locked    bool
	Tags      []string
	CreatedAt time.Time
}

// Storage kinds, in the order the Storage tab shows them.
const (
	StorageObject   = "Object"
	StorageRegistry = "Registry"
	StorageBlock    = "Block"
	StorageLocal    = "Local"
	StorageFile     = "File"
)

// StorageKinds lists the storage kinds in display order.
var StorageKinds = []string{StorageObject, StorageRegistry, StorageBlock, StorageLocal, StorageFile}

// StorageItem is a bucket, registry namespace, block or local volume, or
// file system. Fields that do not apply to a kind stay empty.
type StorageItem struct {
	Kind          string
	ID            string
	Name          string
	ProjectID     string
	Location      string // region or zone
	Status        string
	StatusMessage string
	SizeBytes     uint64 // 0 when unknown (buckets)
	Class         string // volume type
	IOPS          uint32 // block volume performance
	AttachedTo    []string
	Attachments   *int // file system mounts
	Images        *int // registry images
	Public        bool // registry namespace
	Endpoint      string
	Description   string
	Tags          []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// SpendLine is one billed consumption item for a billing period.
type SpendLine struct {
	ProjectID   string
	ProjectName string
	Category    string
	Product     string
	Resource    string
	Quantity    string
	Unit        string
	Amount      float64
	Currency    string
	SKU         string
	ConsumerID  string
}

// Spend is the consumption for one billing period ("YYYY-MM").
type Spend struct {
	Period    string
	Lines     []SpendLine
	UpdatedAt time.Time
}
