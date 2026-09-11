package stayinformed

import "encoding/json"

type Profile struct {
	UserID            string        `json:"id"`
	ApplicationID     string        `json:"appid"`
	AssociationID     string        `json:"association_id"`
	ApplicationName   string        `json:"appname"`
	Timezone          string        `json:"timezone"`
	Language          string        `json:"app_language"`
	Associations      []Association `json:"associations"`
	FirstName         string        `json:"fname"`
	LastName          string        `json:"lname"`
	Successful        string        `json:"success"`
	AttachmentSharing bool          `json:"sharing"`
}

type Association struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Logo string `json:"logo"`
}

type Calendar struct {
	Name               string
	Timezone           string
	LastCalendarUpdate string
	Events             []Event
}

type Event struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Content  string            `json:"content"`
	Venue    string            `json:"venue"`
	Start    string            `json:"start"`
	End      string            `json:"end"`
	Modified string            `json:"modified"`
	Type     string            `json:"type"`
	Status   *string           `json:"status"`
	AllDay   bool              `json:"allDay"`
	Deleted  bool              `json:"deleted"`
	Updated  bool              `json:"updated"`
	Groups   []json.RawMessage `json:"groups"`
}

type NewsPage struct {
	Items         []News          `json:"items"`
	Count         int             `json:"count"`
	Unread        int             `json:"unread"`
	HasDirectNews bool            `json:"has_direct_news"`
	More          json.RawMessage `json:"more"`
}

func (p NewsPage) HasMore(offset, pageSize int) bool {
	if len(p.More) > 0 {
		var boolean bool
		if json.Unmarshal(p.More, &boolean) == nil {
			return boolean
		}
		var text string
		if json.Unmarshal(p.More, &text) == nil {
			switch text {
			case "1", "true", "yes":
				return true
			case "0", "false", "no", "":
				return false
			}
		}
	}
	if p.Count > 0 {
		return (offset+1)*pageSize < p.Count
	}
	return len(p.Items) == pageSize
}

type News struct {
	ID                           string            `json:"id"`
	ObjectID                     string            `json:"_id"`
	Title                        string            `json:"title"`
	Content                      string            `json:"content"`
	Date                         string            `json:"date"`
	DeadlineDate                 string            `json:"deadline_date"`
	Poster                       string            `json:"poster"`
	Type                         string            `json:"type"`
	ReceiverType                 string            `json:"receiver_type"`
	Read                         string            `json:"read"`
	HasAttachments               bool              `json:"attachment"`
	Attachments                  []Attachment      `json:"attachments"`
	Groups                       []json.RawMessage `json:"groups"`
	Important                    bool              `json:"important"`
	Deadline                     bool              `json:"deadline"`
	HTMLContent                  bool              `json:"is_html_content"`
	Hidden                       bool              `json:"is_hidden"`
	Pinned                       bool              `json:"is_pinned"`
	Sharing                      bool              `json:"sharing"`
	ResponseNoLongerPossible     bool              `json:"is_response_no_longer_possible"`
	ResponseType                 string            `json:"response_type"`
	Answered                     bool              `json:"answered"`
	VideoURL                     *string           `json:"videoUrl"`
	VideoThumbnail               *string           `json:"videoThumbnail"`
	VideoType                    *string           `json:"videoType"`
	OverrideAnswered             bool              `json:"override_answered"`
	InAppTranslatorIsActive      bool              `json:"in_app_translator_is_active"`
	ResponseAnswered             bool              `json:"response_answered"`
	IsResponseNoLongerPossibleV2 bool              `json:"response_no_longer_possible"`
}

func (n News) Identifier() string {
	if n.ID != "" {
		return n.ID
	}
	return n.ObjectID
}

type Attachment struct {
	ID         string `json:"id"`
	NewsID     string `json:"news_id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Created    string `json:"created"`
	Modified   string `json:"modified"`
	CreatedID  string `json:"created_id"`
	ModifiedID string `json:"modified_id"`
}

type AttachmentData struct {
	Data        []byte
	ContentType string
	Filename    string
}

type NewsOptions struct {
	Offset           int
	PageSize         int
	Query            string
	Type             string
	ReceiverType     string
	Statuses         []string
	GroupIDs         []string
	IncludeHidden    bool
	OverrideAnswered bool
}
