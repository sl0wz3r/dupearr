package jellyfin

// Wire types (Jellyfin 12.1 answers PascalCase JSON; research §3.2, Appendix A). Only the fields
// Dupearr reads are declared: everything else of an answer is ignored, and the configuration answer
// in particular is decoded into two fields only (it is never logged or stored).

// systemInfoDTO is GET /System/Info and GET /System/Info/Public.
type systemInfoDTO struct {
	ID          string `json:"Id"`
	ServerName  string `json:"ServerName"`
	Version     string `json:"Version"`
	ProductName string `json:"ProductName"`
}

// virtualFolderDTO is one library of GET /Library/VirtualFolders.
type virtualFolderDTO struct {
	Name           string   `json:"Name"`
	CollectionType string   `json:"CollectionType"`
	Locations      []string `json:"Locations"`
	ItemID         string   `json:"ItemId"`
	RefreshStatus  string   `json:"RefreshStatus"`
}

// itemsDTO is GET /Items (and GET /Videos/{id}/AdditionalParts, the same QueryResult shape).
type itemsDTO struct {
	Items            []itemDTO `json:"Items"`
	TotalRecordCount *int      `json:"TotalRecordCount"`
	StartIndex       int       `json:"StartIndex"`
}

// itemDTO is one BaseItemDto row.
type itemDTO struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Path              string            `json:"Path"`
	ProviderIDs       map[string]string `json:"ProviderIds"`
	ProductionYear    int               `json:"ProductionYear"`
	IndexNumber       *int              `json:"IndexNumber"`
	ParentIndexNumber *int              `json:"ParentIndexNumber"`
	IndexNumberEnd    *int              `json:"IndexNumberEnd"`
	SeriesID          string            `json:"SeriesId"`
	SeriesName        string            `json:"SeriesName"`
	LocationType      string            `json:"LocationType"`
	VideoType         string            `json:"VideoType"`
	DateCreated       string            `json:"DateCreated"`
	ParentID          string            `json:"ParentId"`
	ExtraType         string            `json:"ExtraType"`
	MediaSourceCount  *int              `json:"MediaSourceCount"`
	PartCount         *int              `json:"PartCount"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	MediaSources      []mediaSourceDTO  `json:"MediaSources"`
}

// mediaSourceDTO is one entry of MediaSources (research §3.2 table).
type mediaSourceDTO struct {
	ID           string           `json:"Id"`
	Type         string           `json:"Type"` // Default | Grouping | Placeholder
	Path         string           `json:"Path"`
	Size         *int64           `json:"Size"`
	Container    string           `json:"Container"`
	Protocol     string           `json:"Protocol"` // File | Http | …
	IsRemote     bool             `json:"IsRemote"`
	RunTimeTicks *int64           `json:"RunTimeTicks"`
	Bitrate      *int64           `json:"Bitrate"`
	Name         string           `json:"Name"`
	VideoType    string           `json:"VideoType"`
	MediaStreams []mediaStreamDTO `json:"MediaStreams"`
}

// mediaStreamDTO is one entry of MediaStreams. The codec, profile and range spellings Dupearr
// normalises through internal/mediainfo are UNVERIFIED for every format (research Appendix B 11):
// ranking only, never safety.
type mediaStreamDTO struct {
	Type                      string   `json:"Type"` // Video | Audio | Subtitle | EmbeddedImage
	Codec                     string   `json:"Codec"`
	Profile                   string   `json:"Profile"`
	Width                     int      `json:"Width"`
	Height                    int      `json:"Height"`
	BitDepth                  int      `json:"BitDepth"`
	BitRate                   int64    `json:"BitRate"`
	Channels                  int      `json:"Channels"`
	Language                  string   `json:"Language"`
	Title                     string   `json:"Title"`
	DisplayTitle              string   `json:"DisplayTitle"`
	IsDefault                 bool     `json:"IsDefault"`
	IsForced                  bool     `json:"IsForced"`
	IsExternal                bool     `json:"IsExternal"`
	ColorTransfer             string   `json:"ColorTransfer"`
	ColorPrimaries            string   `json:"ColorPrimaries"`
	VideoRangeType            string   `json:"VideoRangeType"`
	DvProfile                 int      `json:"DvProfile"`
	DvBlSignalCompatibilityID int      `json:"DvBlSignalCompatibilityId"`
	Hdr10PlusPresentFlag      bool     `json:"Hdr10PlusPresentFlag"`
	RealFrameRate             *float64 `json:"RealFrameRate"`
	AverageFrameRate          *float64 `json:"AverageFrameRate"`
}

// sessionDTO is one entry of GET /Sessions.
type sessionDTO struct {
	NowPlayingItem *struct {
		ID               string `json:"Id"`
		PrimaryVersionID string `json:"PrimaryVersionId"`
	} `json:"NowPlayingItem"`
	PlayState *struct {
		MediaSourceID string `json:"MediaSourceId"`
		IsPaused      bool   `json:"IsPaused"`
	} `json:"PlayState"`
}

// configurationDTO is the part of GET /System/Configuration Dupearr reads (nothing else of it is
// decoded, logged or stored).
type configurationDTO struct {
	PathSubstitutions []struct {
		From string `json:"From"`
		To   string `json:"To"`
	} `json:"PathSubstitutions"`
	LibraryMonitorDelay *int `json:"LibraryMonitorDelay"`
}

// scheduledTaskDTO is one entry of GET /ScheduledTasks (UNVERIFIED shape, research §8: only used
// to clear a health notice; an unreadable answer leaves the notice).
type scheduledTaskDTO struct {
	Name                string `json:"Name"`
	Key                 string `json:"Key"`
	State               string `json:"State"`
	LastExecutionResult *struct {
		EndTimeUtc string `json:"EndTimeUtc"`
		Status     string `json:"Status"`
	} `json:"LastExecutionResult"`
}

// mediaUpdatesDTO is the body of POST /Library/Media/Updated: paths only.
type mediaUpdatesDTO struct {
	Updates []mediaUpdateDTO `json:"Updates"`
}

type mediaUpdateDTO struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"`
}
