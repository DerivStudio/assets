package processor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	fileLib "github.com/trustwallet/assets-go-libs/file"
	"github.com/trustwallet/assets-go-libs/image"
	"github.com/trustwallet/assets-go-libs/path"
	"github.com/trustwallet/assets-go-libs/validation"
	"github.com/trustwallet/assets-go-libs/validation/info"
	"github.com/trustwallet/assets/internal/file"
	"github.com/trustwallet/go-primitives/address"
	"github.com/trustwallet/go-primitives/coin"
	"github.com/trustwallet/go-primitives/types"

	log "github.com/sirupsen/logrus"
)

func (s *Service) FixJSON(f *file.AssetFile) error {
	return fileLib.FormatJSONFile(f.Path())
}

func (s *Service) FixETHAddressChecksum(f *file.AssetFile) error {
	if !coin.IsEVM(f.Chain().ID) {
		return nil
	}

	assetDir := filepath.Base(f.Path())

	err := validation.ValidateETHForkAddress(f.Chain(), assetDir)
	if err != nil {
		checksum, e := address.EIP55Checksum(assetDir)
		if e != nil {
			return fmt.Errorf("failed to get checksum: %s", e)
		}

		newName := path.GetAssetPath(f.Chain().Handle, checksum)

		if e = os.Rename(f.Path(), newName); e != nil {
			return fmt.Errorf("failed to rename dir: %s", e)
		}

		s.fileService.UpdateFile(f, checksum)

		log.WithField("from", assetDir).
			WithField("to", checksum).
			Debug("Renamed asset")
	}

	return nil
}

func (s *Service) FixLogo(f *file.AssetFile) error {
	width, height, err := image.GetPNGImageDimensions(f.Path())
	if err != nil {
		return err
	}

	var isLogoTooLarge bool
	if width > validation.MaxW || height > validation.MaxH {
		isLogoTooLarge = true
	}

	if isLogoTooLarge {
		log.WithField("path", f.Path()).Debug("Fixing too large image")

		targetW, targetH := calculateTargetDimension(width, height)

		err = image.ResizePNG(f.Path(), targetW, targetH)
		if err != nil {
			return err
		}
	}

	err = validation.ValidateLogoFileSize(f.Path())
	if err != nil { // nolint:staticcheck
		// TODO: Compress images.
	}

	return nil
}

func calculateTargetDimension(width, height int) (targetW, targetH int) {
	widthFloat := float32(width)
	heightFloat := float32(height)

	maxEdge := widthFloat
	if heightFloat > widthFloat {
		maxEdge = heightFloat
	}

	ratio := validation.MaxW / maxEdge

	targetW = int(widthFloat * ratio)
	targetH = int(heightFloat * ratio)

	return targetW, targetH
}

func (s *Service) FixChainInfoJSON(f *file.AssetFile) error {
	chainInfo := info.CoinModel{}

	err := fileLib.ReadJSONFile(f.Path(), &chainInfo)
	if err != nil {
		return err
	}

	expectedType := string(types.Coin)
	if chainInfo.Type == nil || *chainInfo.Type != expectedType {
		chainInfo.Type = &expectedType

		return fileLib.CreateJSONFile(f.Path(), &chainInfo)
	}

	return nil
}

func (s *Service) FixAssetInfo(f *file.AssetFile) error {
	assetInfo := info.AssetModel{}

	err := fileLib.ReadJSONFile(f.Path(), &assetInfo)
	if err != nil {
		return err
	}

	var isModified bool

	// Fix asset type.
	var assetType string
	if assetInfo.Type != nil {
		assetType = *assetInfo.Type
	}

	// We need to skip error check to fix asset type if it's incorrect or empty.
	chain, _ := types.GetChainFromAssetType(assetType)

	expectedTokenType, ok := types.GetTokenType(f.Chain().ID, f.Asset())
	if !ok {
		expectedTokenType = strings.ToUpper(assetType)
	}

	if chain.ID != f.Chain().ID || !strings.EqualFold(assetType, expectedTokenType) {
		assetInfo.Type = &expectedTokenType
		isModified = true
	}

	// Fix asset id.
	assetID := f.Asset()
	if assetInfo.ID == nil || *assetInfo.ID != assetID {
		assetInfo.ID = &assetID
		isModified = true
	}

	expectedExplorerURL, err := coin.GetCoinExploreURL(f.Chain(), f.Asset())
	if err != nil {
		return err
	}

	// Fix asset explorer url.
	if assetInfo.Explorer == nil || !strings.EqualFold(expectedExplorerURL, *assetInfo.Explorer) {
		assetInfo.Explorer = &expectedExplorerURL
		isModified = true
	}

	if isModified {
		return fileLib.CreateJSONFile(f.Path(), &assetInfo)
	}

	return nil
}

func (s *Service) FixTokenList(f *file.AssetFile) error {
	file, err := os.Open(f.Path())
	if err != nil {
		return err
	}
	defer file.Close()

	buf := bytes.NewBuffer(nil)
	if _, err = buf.ReadFrom(file); err != nil {
		return err
	}

	var model TokenList
	err = json.Unmarshal(buf.Bytes(), &model)
	if err != nil {
		return err
	}

	filteredTokens := make([]TokenItem, 0)
	var fixedCounter int

	for _, token := range model.Tokens {
		var assetPath string

		if token.Type == types.Coin {
			assetPath = path.GetChainInfoPath(f.Chain().Handle)
		} else {
			assetPath = path.GetAssetInfoPath(f.Chain().Handle, token.Address)
		}

		infoFile, err := os.Open(assetPath)
		if err != nil {
			return err
		}

		buf := bytes.NewBuffer(nil)
		if _, err = buf.ReadFrom(infoFile); err != nil {
			return err
		}

		infoFile.Close()

		var infoAsset info.AssetModel
		err = json.Unmarshal(buf.Bytes(), &infoAsset)
		if err != nil {
			return err
		}

		if infoAsset.GetStatus() != activeStatus {
			fixedCounter++
			continue
		}

		filteredTokens = append(filteredTokens, token)
	}

	if fixedCounter > 0 {
		return createTokenListJSON(f.Chain(), filteredTokens)
	}

	return nil
}
