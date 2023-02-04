package services

import (
	"context"
	"fmt"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/lmzuccarelli/golang-oci-mirror/pkg/api/v1alpha2"
	"github.com/lmzuccarelli/golang-oci-mirror/pkg/cincinnati"
)

// ReleaseOptions configures either a Full or Diff mirror operation
// on a particular release image.
type ReleaseOptions struct {
	*MirrorOptions
	// insecure indicates whether the source
	// registry is insecure
	insecure bool
	uuid     uuid.UUID
}

// NewReleaseOptions defaults ReleaseOptions.
func NewReleaseOptions(mo *MirrorOptions) *ReleaseOptions {
	relOpts := &ReleaseOptions{
		MirrorOptions: mo,
		uuid:          uuid.New(),
	}
	if mo.SourcePlainHTTP || mo.SourceSkipTLS {
		relOpts.insecure = true
	}
	return relOpts
}

func (o *ReleaseOptions) Run(ctx context.Context, cfg *v1alpha2.ImageSetConfiguration) map[string]struct{} {

	var (
		//srcDir           = filepath.Join(o.Dir, config.SourceDir)
		releaseDownloads = downloads{}
		//mmapping         = image.TypedImageMapping{}
		errs = []error{}
	)

	for _, arch := range cfg.Mirror.Platform.Architectures {

		versionsByChannel := make(map[string]v1alpha2.ReleaseChannel, len(cfg.Mirror.Platform.Channels))

		for _, ch := range cfg.Mirror.Platform.Channels {

			var client cincinnati.Client
			var err error
			switch ch.Type {
			case v1alpha2.TypeOCP:
				client, err = cincinnati.NewOCPClient(o.uuid)
			case v1alpha2.TypeOKD:
				client, err = cincinnati.NewOKDClient(o.uuid)
			default:
				errs = append(errs, fmt.Errorf("invalid platform type %v", ch.Type))
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}

			if len(ch.MaxVersion) == 0 || len(ch.MinVersion) == 0 {

				// Find channel maximum value and only set the minimum as well if heads-only is true
				if len(ch.MaxVersion) == 0 {
					latest, err := cincinnati.GetChannelMinOrMax(ctx, client, arch, ch.Name, false)
					if err != nil {
						errs = append(errs, err)
						continue
					}

					// Update version to release channel
					ch.MaxVersion = latest.String()
					fmt.Printf("Detected minimum version as %s", ch.MaxVersion)
					if len(ch.MinVersion) == 0 && ch.IsHeadsOnly() {
						//min, found := prevChannels[ch.Name]
						//if !found {
						// Starting at a new headsOnly channels
						min := latest.String()
						//}
						ch.MinVersion = min
						fmt.Printf("Detected minimum version as %s\n", ch.MinVersion)
					}
				}

				// Find channel minimum if full is true or just the minimum is not set
				// in the config
				if len(ch.MinVersion) == 0 {
					first, err := cincinnati.GetChannelMinOrMax(ctx, client, arch, ch.Name, true)
					if err != nil {
						errs = append(errs, err)
						continue
					}
					ch.MinVersion = first.String()
					fmt.Printf("Detected minimum version as %s\n", ch.MinVersion)
				}
				versionsByChannel[ch.Name] = ch
			} else {
				// Range is set. Ensure full is true so this
				// is skipped when processing release metadata.
				fmt.Printf("Processing minimum version %s and maximum version %s\n", ch.MinVersion, ch.MaxVersion)
				ch.Full = true
				versionsByChannel[ch.Name] = ch
			}

			downloads, err := o.getChannelDownloads(ctx, client, nil, ch, arch)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			releaseDownloads.Merge(downloads)

		}

		// Update cfg release channels with maximum and minimum versions
		// if applicable
		for i, ch := range cfg.Mirror.Platform.Channels {
			ch, found := versionsByChannel[ch.Name]
			if found {
				cfg.Mirror.Platform.Channels[i] = ch
			}
		}

		if len(cfg.Mirror.Platform.Channels) > 1 {
			client, err := cincinnati.NewOCPClient(o.uuid)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			newDownloads, err := o.getCrossChannelDownloads(ctx, client, arch, cfg.Mirror.Platform.Channels)
			if err != nil {
				errs = append(errs, fmt.Errorf("error calculating cross channel upgrades: %v", err))
				continue
			}
			releaseDownloads.Merge(newDownloads)
		}
	}

	return releaseDownloads
}

type downloads map[string]struct{}

func (d downloads) Merge(in downloads) {
	for k, v := range in {
		_, ok := d[k]
		if ok {
			fmt.Printf("download %s exists", k)
			continue
		}
		d[k] = v
	}
}

var b []byte

/*
func (o *ReleaseOptions) generateReleaseSignatures(ctx context.Context, releaseDownloads downloads) error {

	httpClientConstructor := sigstore.NewCachedHTTPClientConstructor(o.HTTPClient, nil)

	manifests, err := manifest.ParseManifests(bytes.NewReader(b))

	if err != nil {
		return err
	}

	// Attempt to load a verifier as defined by the release being mirrored
	imageVerifier, err := verify.NewFromManifests(manifests, httpClientConstructor.HTTPClient)

	if err != nil {
		return err
	}

	signatureBasePath := filepath.Join(o.Dir, config.SourceDir, config.ReleaseSignatureDir)
	if err := os.MkdirAll(signatureBasePath, 0750); err != nil {
		return err
	}

	for image := range releaseDownloads {
		digest := strings.Split(image, "@")[1]

		if err := imageVerifier.Verify(ctx, digest); err != nil {
			// This may be a OKD release image hence no valid signature
			klog.Warningf("An image was retrieved that failed verification: %v", err)
			continue
		}

		cmData, err := verify.GetSignaturesAsConfigmap(digest, imageVerifier.Signatures()[digest])
		if err != nil {
			return err
		}

		cmDataBytes, err := util.ConfigMapAsBytes(cmData)
		if err != nil {
			return err
		}

		fileName, err := createSignatureFileName(digest)
		if err != nil {
			return err
		}

		signaturePath := filepath.Join(signatureBasePath, fileName)

		if err := os.WriteFile(signaturePath, cmDataBytes, 0640); err != nil {
			return err
		}

	}

	return nil
}
*/

// getDownloads will prepare the downloads map for mirroring
func (o *ReleaseOptions) getChannelDownloads(ctx context.Context, c cincinnati.Client, lastChannels []v1alpha2.ReleaseChannel, channel v1alpha2.ReleaseChannel, arch string) (downloads, error) {
	allDownloads := downloads{}

	var prevChannel v1alpha2.ReleaseChannel
	for _, ch := range lastChannels {
		if ch.Name == channel.Name {
			prevChannel = ch
		}
	}

	if prevChannel.Name != "" {
		// If the requested min version is less than the previous, add downloads
		if prevChannel.MinVersion > channel.MinVersion {
			first, err := semver.Parse(channel.MinVersion)
			if err != nil {
				return allDownloads, err
			}
			last, err := semver.Parse(prevChannel.MinVersion)
			if err != nil {
				return allDownloads, err
			}
			current, newest, updates, err := cincinnati.CalculateUpgrades(ctx, c, arch, channel.Name, channel.Name, first, last)
			if err != nil {
				return allDownloads, err
			}
			newDownloads := gatherUpdates(current, newest, updates)
			allDownloads.Merge(newDownloads)
		}

		// If the requested max version is more than the previous, add downloads
		if prevChannel.MaxVersion < channel.MaxVersion {
			first, err := semver.Parse(prevChannel.MaxVersion)
			if err != nil {
				return allDownloads, err
			}
			last, err := semver.Parse(channel.MinVersion)
			if err != nil {
				return allDownloads, err
			}
			current, newest, updates, err := cincinnati.CalculateUpgrades(ctx, c, arch, channel.Name, channel.Name, first, last)
			if err != nil {
				return allDownloads, err
			}
			newDownloads := gatherUpdates(current, newest, updates)
			allDownloads.Merge(newDownloads)
		}
	}

	// Plot between min and max of channel
	first, err := semver.Parse(channel.MinVersion)
	if err != nil {
		return allDownloads, err
	}
	last, err := semver.Parse(channel.MaxVersion)
	if err != nil {
		return allDownloads, err
	}

	var newDownloads downloads
	if channel.ShortestPath {
		current, newest, updates, err := cincinnati.CalculateUpgrades(ctx, c, arch, channel.Name, channel.Name, first, last)
		if err != nil {
			return allDownloads, err
		}
		newDownloads = gatherUpdates(current, newest, updates)

	} else {
		lowRange, err := semver.ParseRange(fmt.Sprintf(">=%s", first))
		if err != nil {
			return allDownloads, err
		}
		highRange, err := semver.ParseRange(fmt.Sprintf("<=%s", last))
		if err != nil {
			return allDownloads, err
		}
		versions, err := cincinnati.GetUpdatesInRange(ctx, c, channel.Name, arch, highRange.AND(lowRange))
		if err != nil {
			return allDownloads, err
		}
		newDownloads = gatherUpdates(cincinnati.Update{}, cincinnati.Update{}, versions)
	}
	allDownloads.Merge(newDownloads)

	return allDownloads, nil
}

// getCrossChannelDownloads will determine required downloads between channel versions (for OCP only)
func (o *ReleaseOptions) getCrossChannelDownloads(ctx context.Context, ocpClient cincinnati.Client, arch string, channels []v1alpha2.ReleaseChannel) (downloads, error) {
	// Strip any OKD channels from the list

	var ocpChannels []v1alpha2.ReleaseChannel
	for _, ch := range channels {
		if ch.Type == v1alpha2.TypeOCP {
			ocpChannels = append(ocpChannels, ch)
		}
	}
	// If no other channels exist, return no downloads
	if len(ocpChannels) == 0 {
		return downloads{}, nil
	}

	firstCh, first, err := cincinnati.FindRelease(ocpChannels, true)
	if err != nil {
		return downloads{}, fmt.Errorf("failed to find minimum release version: %v", err)
	}
	lastCh, last, err := cincinnati.FindRelease(ocpChannels, false)
	if err != nil {
		return downloads{}, fmt.Errorf("failed to find maximum release version: %v", err)
	}
	current, newest, updates, err := cincinnati.CalculateUpgrades(ctx, ocpClient, arch, firstCh, lastCh, first, last)
	if err != nil {
		return downloads{}, fmt.Errorf("failed to get upgrade graph: %v", err)
	}
	return gatherUpdates(current, newest, updates), nil
}

func gatherUpdates(current, newest cincinnati.Update, updates []cincinnati.Update) downloads {
	releaseDownloads := downloads{}
	for _, update := range updates {
		fmt.Printf("Found update %s\n", update.Version)
		releaseDownloads[update.Image] = struct{}{}
	}

	if current.Image != "" {
		releaseDownloads[current.Image] = struct{}{}
	}

	if newest.Image != "" {
		releaseDownloads[newest.Image] = struct{}{}
	}

	return releaseDownloads
}

/*

func (o *ReleaseOptions) newMirrorReleaseOptions(fileDir string) (*release.MirrorOptions, error) {
	opts := release.NewMirrorOptions(o.IOStreams)
	opts.DryRun = o.DryRun
	opts.ToDir = fileDir

	opts.SecurityOptions.Insecure = o.insecure
	opts.SecurityOptions.SkipVerification = o.SkipVerification

	regctx, err := image.NewContext(o.SkipVerification)
	if err != nil {
		return nil, fmt.Errorf("error creating registry context: %v", err)
	}
	opts.SecurityOptions.CachedContext = regctx

	return opts, nil
}

// getMapping will run release mirror with ToMirror set to true to get mapping information
func (o *ReleaseOptions) getMapping(opts *release.MirrorOptions) (image.TypedImageMapping, error) {
	mappingPath := filepath.Join(o.Dir, mappingFile)
	file, err := os.Create(filepath.Clean(mappingPath))
	defer os.Remove(mappingPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	opts.IOStreams.Out = file
	opts.ToMirror = true
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if err := opts.Run(); err != nil {
		return nil, err
	}

	mappings, err := image.ReadImageMapping(mappingPath, " ", v1alpha2.TypeOCPReleaseContent)
	if err != nil {
		return nil, err
	}

	releaseImageRef, err := image.ParseTypedImage(opts.From, v1alpha2.TypeOCPReleaseContent)
	if err != nil {
		return nil, err
	}
	dstReleaseRef, ok := mappings[releaseImageRef]
	if !ok {
		return nil, fmt.Errorf("release images %s not found in mapping", opts.From)
	}
	// Remove and readd the release image to the
	// mapping with the correct repo name and image type.
	mappings.Remove(releaseImageRef)
	dstReleaseRef.Ref.Name = releaseRepo
	mappings.Add(releaseImageRef.TypedImageReference, dstReleaseRef.TypedImageReference, v1alpha2.TypeOCPRelease)

	return mappings, nil
}

func (o *ReleaseOptions) generateReleaseSignatures(ctx context.Context, releaseDownloads downloads) error {

	httpClientConstructor := sigstore.NewCachedHTTPClientConstructor(o.HTTPClient, nil)

	manifests, err := manifest.ParseManifests(bytes.NewReader(b))

	if err != nil {
		return err
	}

	// Attempt to load a verifier as defined by the release being mirrored
	imageVerifier, err := verify.NewFromManifests(manifests, httpClientConstructor.HTTPClient)

	if err != nil {
		return err
	}

	signatureBasePath := filepath.Join(o.Dir, config.SourceDir, config.ReleaseSignatureDir)
	if err := os.MkdirAll(signatureBasePath, 0750); err != nil {
		return err
	}

	for image := range releaseDownloads {
		digest := strings.Split(image, "@")[1]

		if err := imageVerifier.Verify(ctx, digest); err != nil {
			// This may be a OKD release image hence no valid signature
			klog.Warningf("An image was retrieved that failed verification: %v", err)
			continue
		}

		cmData, err := verify.GetSignaturesAsConfigmap(digest, imageVerifier.Signatures()[digest])
		if err != nil {
			return err
		}

		cmDataBytes, err := util.ConfigMapAsBytes(cmData)
		if err != nil {
			return err
		}

		fileName, err := createSignatureFileName(digest)
		if err != nil {
			return err
		}

		signaturePath := filepath.Join(signatureBasePath, fileName)

		if err := os.WriteFile(signaturePath, cmDataBytes, 0640); err != nil {
			return err
		}

	}

	return nil
}

func createSignatureFileName(digest string) (string, error) {
	parts := strings.SplitN(digest, ":", 3)
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) == 0 {
		return "", fmt.Errorf("the provided digest, %s, must be of the form ALGO:HASH", digest)
	}
	algo, hash := parts[0], parts[1]

	if len(hash) > maxDigestHashLen {
		hash = hash[:maxDigestHashLen]
	}
	return fmt.Sprintf(signatureFileNameFmt, algo, hash), nil
}
*/
