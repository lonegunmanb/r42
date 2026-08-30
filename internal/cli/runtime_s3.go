package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lonegunmanb/golden"
	"github.com/lonegunmanb/r42/internal/plan"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/zclconf/go-cty/cty"
)

func (f *runtimeFactory) newS3FolderBlock(ctx context.Context, node plan.NodeSpec) (golden.ApplyBlock, error) {
	providerConfig, folderConfig, err := s3spec.DecodeFolderPlan(node.Config)
	if err != nil {
		return nil, err
	}
	return &s3FolderApplyBlock{
		BaseBlock: new(golden.BaseBlock), ctx: ctx, address: node.Address, run: f.run,
		provider: providerConfig, folder: folderConfig, serviceFactory: f.s3ServiceFactory,
		lookup: f.s3EnvLookup, publish: f.publish,
	}, nil
}

type s3FolderApplyBlock struct {
	*golden.BaseBlock
	ctx            context.Context
	address        string
	run            interface{ Directory() string }
	provider       s3spec.ProviderConfig
	folder         s3spec.FolderConfig
	serviceFactory internals3.ServiceFactory
	lookup         internals3.EnvLookup
	publish        func(string, cty.Value)
}

func (*s3FolderApplyBlock) Type() string            { return "" }
func (*s3FolderApplyBlock) BlockType() string       { return "s3_folder" }
func (*s3FolderApplyBlock) AddressLength() int      { return 2 }
func (*s3FolderApplyBlock) CanExecutePrePlan() bool { return false }
func (b *s3FolderApplyBlock) Address() string       { return b.address }

func (b *s3FolderApplyBlock) Apply() error {
	sourceRoot, err := s3spec.ResolveSource(b.folder.Source, b.run.Directory())
	if err != nil {
		return err
	}
	files, err := s3spec.ListSourceFiles(sourceRoot, b.folder.Exclude)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		b.publishResult(0)
		return nil
	}
	lookup := b.lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	client, err := internals3.NewClient(b.provider, lookup, b.serviceFactory)
	if err != nil {
		return err
	}
	retry, err := internals3.MergeRetry(internals3.DefaultRetryPolicy(), b.provider.Retry)
	if err != nil {
		return fmt.Errorf("s3 provider retry: %w", err)
	}
	retry, err = internals3.MergeRetry(retry, b.folder.Retry)
	if err != nil {
		return fmt.Errorf("s3 folder retry: %w", err)
	}
	versioning, err := internals3.BucketVersioningStatus(b.ctx, client, b.folder.Bucket, retry)
	if err != nil {
		return err
	}
	uploaded, err := internals3.UploadFiles(b.ctx, client, b.folder.Bucket, b.folder.Prefix, files, retry)
	if err != nil {
		available, cleanupErr := internals3.RollbackUploaded(context.WithoutCancel(b.ctx), client, b.folder.Bucket, versioning, uploaded, retry)
		if !available {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remote rollback is unavailable because bucket versioning is %q", versioning))
		}
		return internals3.UploadFailure(sourceRoot, b.folder.Bucket, b.folder.Prefix, "", err, cleanupErr)
	}
	b.publishResult(len(uploaded))
	return nil
}

func (b *s3FolderApplyBlock) publishResult(objectCount int) {
	root := "s3://" + b.folder.Bucket
	if b.folder.Prefix != "" {
		root += "/" + b.folder.Prefix
	}
	b.publish(b.address, cty.ObjectVal(map[string]cty.Value{"result": cty.ObjectVal(map[string]cty.Value{
		"bucket": cty.StringVal(b.folder.Bucket), "prefix": cty.StringVal(b.folder.Prefix),
		"root": cty.StringVal(root), "object_count": cty.NumberIntVal(int64(objectCount)),
	})}))
}
