// package vrf_test verifies correct and up-to-date generation of golang wrappers
// for solidity contracts. See go_generate.go for the actual generation.
package vrf_test

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"chainlink/core/utils"

	"github.com/pkg/errors"
	"github.com/tidwall/gjson"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractVersion records information about the solidity compiler artifact a
// golang contract wrapper package depends on.
type contractVersion struct {
	// path to compiler artifact used by generate.sh to create wrapper package
	compilerArtifactPath string
	// hash of the artifact at the timem the wrapper was last generated
	hash string
}

// integratedVersion carries the full versioning information checked in this test
type integratedVersion struct {
	// { golang-pkg-name: version_info }
	contractVersions map[string]contractVersion
}

// TestCheckContractHashesFromLastGoGenerate compares the metadata recorded by
// record_versions.sh, and fails if it indicates that the corresponding golang
// wrappers are out of date with respect to the solidty contracts they wrap. See
// record_versions.sh for description of file format.
func TestCheckContractHashesFromLastGoGenerate(t *testing.T) {
	versions := readVersionsDB(t)
	for _, contractVersionInfo := range versions.contractVersions {
		compareCurrentCompilerAritfactAgainstRecordsAndSoliditySources(
			t, contractVersionInfo)
	}
}

// compareCurrentCompilerAritfactAgainstRecordsAndSoliditySources checks that
// the file at each contractVersion.compilerArtifactPath hashes to its
// contractVersion.hash, and that the solidity source code recorded in the
// compiler artifact matches the current solidity contracts.
//
// Most of the compiler artifacts should contain output from sol-compiler, or
// "yarn compile". The relevant parts of its schema are
//
//    { "sourceCodes": { "<filePath>": "<code>", ... } }
//
// where <filePath> is the path to the contract, below the truffle contracts/
// directory, and <code> is the source code of the contract at the time the JSON
// file was generated.
func compareCurrentCompilerAritfactAgainstRecordsAndSoliditySources(
	t *testing.T, versionInfo contractVersion,
) {
	apath := versionInfo.compilerArtifactPath
	// check the compiler outputs (abi and bytecode object) haven't changed
	compilerJSON, err := ioutil.ReadFile(apath)
	require.NoError(t, err, "failed to read JSON compiler artifact %s", apath)
	abiPath := "compilerOutput.abi"
	binPath := "compilerOutput.evm.bytecode.object"
	isLINKCompilerOutput :=
		path.Base(versionInfo.compilerArtifactPath) == "LinkToken.json"
	if isLINKCompilerOutput {
		abiPath = "abi"
		binPath = "bytecode"
	}
	// Normalize the whitespace in the ABI JSON
	abiBytes := stripWhitespace(gjson.GetBytes(compilerJSON, abiPath).String(), "")
	binBytes := gjson.GetBytes(compilerJSON, binPath).String()
	if !isLINKCompilerOutput {
		// Remove the varying contract metadata, as in ./generation/generate.sh
		binBytes = binBytes[:len(binBytes)-106]
	}
	hasher := sha256.New()
	hashMsg := string(abiBytes+binBytes) + "\n" // newline from <<< in record_versions.sh
	_, err = io.WriteString(hasher, hashMsg)
	require.NoError(t, err, "failed to hash compiler artifact %s", apath)
	recompileCommand := fmt.Sprintf("`%s && go generate`", compileCommand(t))
	assert.Equal(t, versionInfo.hash, fmt.Sprintf("%x", hasher.Sum(nil)),
		"compiler artifact %s has changed; please rerun %s for the vrf package",
		apath, recompileCommand)

	var artifact struct {
		Sources map[string]string `json:"sourceCodes"`
	}
	require.NoError(t, json.Unmarshal(compilerJSON, &artifact),
		"could not read compiler artifact %s", apath)

	if !isLINKCompilerOutput { // No need to check contract source for LINK token
		// Check that each of the contract source codes hasn't changed
		soliditySourceRoot := filepath.Dir(filepath.Dir(filepath.Dir(apath)))
		contractPath := filepath.Join(soliditySourceRoot, "src", "v0.6")
		for sourcePath, sourceCode := range artifact.Sources { // compare to current source
			sourcePath = filepath.Join(contractPath, sourcePath)
			actualSource, err := ioutil.ReadFile(sourcePath)
			require.NoError(t, err, "could not read "+sourcePath)
			assert.Equal(t, string(actualSource), sourceCode,
				"%s has changed; please rerun %s for the vrf package",
				sourcePath, recompileCommand)
		}
	}
}

func versionsDBLineReader() (*bufio.Scanner, error) {
	dirOfThisTest, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dBBasename := "generated-wrapper-dependency-versions-do-not-edit.txt"
	dbPath := filepath.Join(dirOfThisTest, "generation", dBBasename)
	versionsDBFile, err := os.Open(dbPath)
	if err != nil {
		return nil, errors.Wrapf(err, "could not open versions database")
	}
	return bufio.NewScanner(versionsDBFile), nil

}

// readVersionsDB populates an integratedVersion with all the info in the
// versions DB
func readVersionsDB(t *testing.T) integratedVersion {
	rv := integratedVersion{}
	rv.contractVersions = make(map[string]contractVersion)
	db, err := versionsDBLineReader()
	require.NoError(t, err)
	for db.Scan() {
		line := strings.Fields(db.Text())
		require.True(t, strings.HasSuffix(line[0], ":"),
			`each line in versions.txt should start with "$TOPIC:"`)
		topic := stripTrailingColon(line[0], "")

		require.Len(t, line, 3,
			`"%s" should have three elements "<pkgname>: <compiler-artifact-path> <compiler-artifact-hash>"`,
			db.Text())
		_, alreadyExists := rv.contractVersions[topic]
		require.False(t, alreadyExists, `topic "%s" already mentioned!`, topic)
		rv.contractVersions[topic] = contractVersion{
			compilerArtifactPath: line[1], hash: line[2],
		}
	}
	return rv
}

// Ensure that solidity compiler artifacts are present before running this test,
// by compiling them if necessary.
func init() {
	db, err := versionsDBLineReader()
	if err != nil {
		panic(err)
	}
	var solidityArtifactsMissing []string
	for db.Scan() {
		line := strings.Fields(db.Text())
		if os.IsNotExist(utils.JustError(os.Stat(line[1]))) {
			solidityArtifactsMissing = append(solidityArtifactsMissing, line[1])
		}
	}
	if len(solidityArtifactsMissing) == 0 {
		return
	}
	fmt.Printf("some solidity artifacts missing (%s); rebuilding...",
		solidityArtifactsMissing)
	compileCmd := strings.Fields(compileCommand(nil))
	cmd := exec.Command(compileCmd[0], compileCmd[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic(err)
	}
}

var (
	stripWhitespace    = regexp.MustCompile(`\s+`).ReplaceAllString
	stripTrailingColon = regexp.MustCompile(":$").ReplaceAllString
)

// compileCommand() is a shell command which compiles chainlink's solidity
// contracts.
func compileCommand(t *testing.T) string {
	cmd, err := ioutil.ReadFile("./generation/compile_command.txt")
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return string(cmd)
}
