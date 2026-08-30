package spec

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/lonegunmanb/golden"
	"github.com/zclconf/go-cty/cty"
)

var (
	_ golden.SingleValueBlock = (*ProviderBlock)(nil)
	_ golden.PlanBlock        = (*FolderBlock)(nil)
	_ golden.ApplyBlock       = (*FolderBlock)(nil)
)

type RetryOverride struct {
	MaxRetries        *int
	Interval          *time.Duration
	MaxInterval       *time.Duration
	ErrorMessageRegex []string
}

func (r RetryOverride) Validate() error {
	if r.MaxRetries != nil && *r.MaxRetries < 0 {
		return errors.New("max retries must not be negative")
	}
	if r.Interval != nil && *r.Interval < 0 {
		return errors.New("retry interval must not be negative")
	}
	if r.MaxInterval != nil && *r.MaxInterval < 0 {
		return errors.New("maximum retry interval must not be negative")
	}
	if r.Interval != nil && r.MaxInterval != nil && *r.MaxInterval < *r.Interval {
		return errors.New("maximum retry interval must not be less than retry interval")
	}
	return nil
}

type ProviderConfig struct {
	Endpoint        string
	Region          string
	AccessKey       *string
	AccessKeyRef    *string
	SecretKey       *string
	SecretKeyRef    *string
	SessionToken    *string
	SessionTokenRef *string
	ForcePathStyle  bool
	Retry           RetryOverride
}

func (c ProviderConfig) Validate() error {
	if strings.TrimSpace(c.Region) == "" {
		return errors.New("s3 provider region is required")
	}
	if c.Endpoint != "" {
		endpoint, err := url.Parse(c.Endpoint)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
			return errors.New("s3 provider endpoint must be an HTTPS URL")
		}
	}
	for name, pair := range map[string][2]*string{
		"access_key":    {c.AccessKey, c.AccessKeyRef},
		"secret_key":    {c.SecretKey, c.SecretKeyRef},
		"session_token": {c.SessionToken, c.SessionTokenRef},
	} {
		if pair[0] != nil && pair[1] != nil {
			return fmt.Errorf("s3 provider %s and %s_ref cannot both be set", name, name)
		}
		for _, value := range pair {
			if value != nil && strings.TrimSpace(*value) == "" {
				return fmt.Errorf("s3 provider %s must not be empty", name)
			}
		}
	}
	hasAccessKey := c.AccessKey != nil || c.AccessKeyRef != nil
	hasSecretKey := c.SecretKey != nil || c.SecretKeyRef != nil
	if hasAccessKey != hasSecretKey {
		return errors.New("s3 provider access_key and secret_key must be configured together")
	}
	if c.SessionToken != nil || c.SessionTokenRef != nil {
		if !hasAccessKey {
			return errors.New("s3 provider session_token requires access_key and secret_key")
		}
	}
	return c.Retry.Validate()
}

type FolderConfig struct {
	Provider cty.Value
	Bucket   string
	Source   string
	Prefix   string
	Exclude  []string
	Retry    RetryOverride
}

func (c FolderConfig) Validate() error {
	if strings.TrimSpace(c.Bucket) == "" {
		return errors.New("s3 folder bucket is required")
	}
	if err := validateProviderReference(c.Provider); err != nil {
		return err
	}
	if strings.TrimSpace(c.Source) == "" {
		return errors.New("s3 folder source is required")
	}
	if c.Prefix != "" {
		if strings.HasPrefix(c.Prefix, "/") {
			return errors.New("s3 folder prefix must not start with slash")
		}
		if strings.HasSuffix(c.Prefix, "/") {
			return errors.New("s3 folder prefix must not end with slash")
		}
		if strings.Contains(c.Prefix, `\`) {
			return errors.New("s3 folder prefix must not contain backslash")
		}
		for segment := range strings.SplitSeq(c.Prefix, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return errors.New("s3 folder prefix must not contain empty, . or .. segments")
			}
			for _, r := range segment {
				if unicode.IsControl(r) {
					return errors.New("s3 folder prefix must not contain control characters")
				}
			}
		}
	}
	return c.Retry.Validate()
}

func validateProviderReference(value cty.Value) error {
	if value == cty.NilVal || value.IsNull() {
		return errors.New("s3 folder provider is required")
	}
	unmarked, _ := value.UnmarkDeep()
	if !unmarked.IsKnown() || !unmarked.Type().IsObjectType() || !unmarked.Type().HasAttribute("address") || !unmarked.Type().HasAttribute("kind") {
		return errors.New("s3 folder provider must be an s3_provider reference")
	}
	if !unmarked.GetAttr("address").Type().Equals(cty.String) || !unmarked.GetAttr("kind").Type().Equals(cty.String) ||
		unmarked.GetAttr("kind").AsString() != "s3_provider" {
		return errors.New("s3 folder provider must be an s3_provider reference")
	}
	return nil
}

type RetryBlock struct {
	MaxRetries         *int     `hcl:"max_retries,optional"`
	IntervalSeconds    *int     `hcl:"interval_seconds,optional"`
	MaxIntervalSeconds *int     `hcl:"max_interval_seconds,optional"`
	ErrorMessageRegex  []string `hcl:"error_message_regex,optional"`
}

func (r RetryBlock) override() (RetryOverride, error) {
	o := RetryOverride{MaxRetries: r.MaxRetries, ErrorMessageRegex: append([]string(nil), r.ErrorMessageRegex...)}
	if r.IntervalSeconds != nil {
		value := time.Duration(*r.IntervalSeconds) * time.Second
		o.Interval = &value
	}
	if r.MaxIntervalSeconds != nil {
		value := time.Duration(*r.MaxIntervalSeconds) * time.Second
		o.MaxInterval = &value
	}
	return o, o.Validate()
}

type ProviderBlock struct {
	*golden.BaseBlock
	Endpoint        string       `hcl:"endpoint,optional"`
	Region          string       `hcl:"region"`
	AccessKey       *string      `hcl:"access_key,optional"`
	AccessKeyRef    *string      `hcl:"access_key_ref,optional"`
	SecretKey       *string      `hcl:"secret_key,optional"`
	SecretKeyRef    *string      `hcl:"secret_key_ref,optional"`
	SessionToken    *string      `hcl:"session_token,optional"`
	SessionTokenRef *string      `hcl:"session_token_ref,optional"`
	ForcePathStyle  bool         `hcl:"force_path_style,optional"`
	RetryBlocks     []RetryBlock `hcl:"retry,block"`
	planned         ProviderConfig
}

func (*ProviderBlock) Type() string            { return "" }
func (*ProviderBlock) BlockType() string       { return "s3_provider" }
func (*ProviderBlock) AddressLength() int      { return 2 }
func (*ProviderBlock) CanExecutePrePlan() bool { return false }

func (b *ProviderBlock) Value() cty.Value { return ProviderBlockValue(b.Address(), b.planned) }

func (b *ProviderBlock) ExecuteDuringPlan() error {
	if len(b.RetryBlocks) > 1 {
		return errors.New("s3 provider must have at most one retry block")
	}
	c := ProviderConfig{Endpoint: b.Endpoint, Region: b.Region, AccessKey: clone(b.AccessKey), AccessKeyRef: clone(b.AccessKeyRef), SecretKey: clone(b.SecretKey), SecretKeyRef: clone(b.SecretKeyRef), SessionToken: clone(b.SessionToken), SessionTokenRef: clone(b.SessionTokenRef), ForcePathStyle: b.ForcePathStyle}
	var err error
	if len(b.RetryBlocks) == 1 {
		c.Retry, err = b.RetryBlocks[0].override()
		if err != nil {
			return err
		}
	}
	if err = c.Validate(); err != nil {
		return err
	}
	b.planned = c
	return nil
}

func (b *ProviderBlock) ProviderConfig() ProviderConfig { return b.planned }

type FolderBlock struct {
	*golden.BaseBlock
	Provider    cty.Value    `hcl:"provider"`
	Bucket      string       `hcl:"bucket"`
	Source      string       `hcl:"source"`
	Prefix      string       `hcl:"prefix,optional"`
	Exclude     []string     `hcl:"exclude,optional"`
	RetryBlocks []RetryBlock `hcl:"retry,block"`
	planned     FolderConfig
}

func (*FolderBlock) Type() string            { return "" }
func (*FolderBlock) BlockType() string       { return "s3_folder" }
func (*FolderBlock) AddressLength() int      { return 2 }
func (*FolderBlock) CanExecutePrePlan() bool { return false }

func (b *FolderBlock) ExecuteDuringPlan() error {
	if len(b.RetryBlocks) > 1 {
		return errors.New("s3 folder must have at most one retry block")
	}
	c := FolderConfig{Provider: b.Provider, Bucket: b.Bucket, Source: b.Source, Prefix: b.Prefix, Exclude: append([]string(nil), b.Exclude...)}
	var err error
	if len(b.RetryBlocks) == 1 {
		c.Retry, err = b.RetryBlocks[0].override()
		if err != nil {
			return err
		}
	}
	if err = c.Validate(); err != nil {
		return err
	}
	b.planned = c
	return nil
}

func (b *FolderBlock) Apply() error {
	applier, ok := b.Config().(interface{ ApplyBlock(string) error })
	if !ok {
		return fmt.Errorf("s3 folder %q requires an r42 apply config", b.Name())
	}
	return applier.ApplyBlock(b.Address())
}
func (b *FolderBlock) FolderConfig() FolderConfig { return b.planned }
func (b *FolderBlock) Value() cty.Value           { return FolderBlockValue(b.Address(), b.planned) }

var resultType = cty.Object(map[string]cty.Type{"bucket": cty.String, "prefix": cty.String, "root": cty.String, "object_count": cty.Number})

func ProviderBlockValue(address string, c ProviderConfig) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal(address), "kind": cty.StringVal("s3_provider"),
		"endpoint": cty.StringVal(c.Endpoint), "region": cty.StringVal(c.Region),
		"force_path_style": cty.BoolVal(c.ForcePathStyle),
		"access_key":       sensitive(c.AccessKey), "access_key_ref": cty.StringVal(pointerString(c.AccessKeyRef)),
		"secret_key": sensitive(c.SecretKey), "secret_key_ref": cty.StringVal(pointerString(c.SecretKeyRef)),
		"session_token": sensitive(c.SessionToken), "session_token_ref": cty.StringVal(pointerString(c.SessionTokenRef)),
	})
}

func FolderBlockValue(address string, c FolderConfig) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"address": cty.StringVal(address), "kind": cty.StringVal("s3_folder"),
		"provider": c.Provider, "bucket": cty.StringVal(c.Bucket), "source": cty.StringVal(c.Source),
		"prefix": cty.StringVal(c.Prefix), "exclude": stringList(c.Exclude), "result": cty.UnknownVal(resultType),
	})
}

func clone(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sensitive(value *string) cty.Value {
	if value == nil {
		return cty.NullVal(cty.String)
	}
	return cty.StringVal("<sensitive>").Mark("sensitive")
}

func stringList(values []string) cty.Value {
	if len(values) == 0 {
		return cty.ListValEmpty(cty.String)
	}
	result := make([]cty.Value, len(values))
	for i, value := range values {
		result[i] = cty.StringVal(value)
	}
	return cty.ListVal(result)
}
