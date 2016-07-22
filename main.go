package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/getcarina/carina/version"
	"github.com/getcarina/libcarina"

	"github.com/getcarina/carina/adapters"
)

// Application is, our, well, application
type Application struct {
	*Context
	*kingpin.Application
}

// Command is a command that interacts with a cluster service
type Command struct {
	*Context
	*kingpin.CmdClause
	CloudType string
}

// Context for the App
type Context struct {
	ClusterClient *libcarina.ClusterClient
	TabWriter     *tabwriter.Writer
	Username      string
	APIKey        string
	Password      string
	Project       string
	Domain        string
	Region        string
	Endpoint      string
	CacheEnabled  bool
	Cache         *Cache
}

// ClusterCommand is a Command with a ClusterName set
type ClusterCommand struct {
	*Command
	ClusterName string
}

// CredentialsCommand keeps context about the download command
type CredentialsCommand struct {
	*ClusterCommand
	Path   string
	Silent bool
}

// ShellCommand keeps context about the currently executing shell
type ShellCommand struct {
	*CredentialsCommand
	Shell string
}

// WaitClusterCommand is simply a ClusterCommand that waits for cluster creation
type WaitClusterCommand struct {
	*ClusterCommand
	// Whether to wait until the cluster is created (or errored)
	Wait bool
}

// CreateCommand keeps context about the create command
type CreateCommand struct {
	*WaitClusterCommand

	// Options passed along to Carina's API
	Nodes     int
	AutoScale bool

	// TODO: See if setting flavor or image makes sense, even if the API takes it
	// Flavor    string
	// Image     string
}

// GrowCommand keeps context about the number of nodes to scale by
type GrowCommand struct {
	*ClusterCommand
	Nodes int
}

// AutoScaleCommand keeps context about a cluster command
type AutoScaleCommand struct {
	*ClusterCommand
	AutoScale string
}

// AutoScaleOn is the "give me autoscale on this cluster" string for the cli
const AutoScaleOn = "on"

// AutoScaleOff is the "turn off autoscale on this cluster" string for the cli
const AutoScaleOff = "off"

// CarinaUserNameEnvVar is the Carina username environment variable (1st)
const CarinaUserNameEnvVar = "CARINA_USERNAME"

// RackspaceUserNameEnvVar is the Rackspace username environment variable (2nd)
const RackspaceUserNameEnvVar = "RS_USERNAME"

// OpenStackUserNameEnvVar is the OpenStack username environment variable (3nd)
const OpenStackUserNameEnvVar = "OS_USERNAME"

// CarinaAPIKeyEnvVar is the Carina API key environment variable (1st)
const CarinaAPIKeyEnvVar = "CARINA_APIKEY"

// RackspaceAPIKeyEnvVar is the Rackspace API key environment variable (2nd)
const RackspaceAPIKeyEnvVar = "RS_API_KEY"

// OpenStackPasswordEnvVar is OpenStack password environment variable
// When set, this instructs the cli to communicate with Carina (private cloud) instead of the default Carina (public cloud)
const OpenStackPasswordEnvVar = "OS_PASSWORD"

// OpenStackAuthURLEnvVar is the OpenStack Identity URL (v2 and v3 supported)
const OpenStackAuthURLEnvVar = "OS_AUTH_URL"

// OpenStackProjectEnvVar is the OpenStack project name, required for identity v3
const OpenStackProjectEnvVar = "OS_PROJECT_NAME"

// OpenStackDomainEnvVar is the OpenStack domain name, optional for identity v3
const OpenStackDomainEnvVar = "OS_DOMAIN_NAME"

// OpenStackRegionEnvVar is the OpenStack domain name, optional for identity v3
const OpenStackRegionEnvVar = "OS_REGION_NAME"

// New creates a new Application
func New() *Application {

	app := kingpin.New("carina", "command line interface to launch and work with Docker Swarm clusters")
	app.Version(VersionString())

	baseDir, err := CarinaCredentialsBaseDir()
	if err != nil {
		panic(err)
	}
	envHelp := fmt.Sprintf(`Environment Variables:
  CARINA_HOME
    directory that stores your cluster tokens and credentials
    current setting: %s
`, baseDir)
	app.UsageTemplate(kingpin.DefaultUsageTemplate + envHelp)

	cap := new(Application)
	ctx := new(Context)

	cap.Application = app

	cap.Context = ctx

	cap.Flag("username", "Carina username [CARINA_USERNAME/RS_USERNAME/OS_USERNAME]").StringVar(&ctx.Username)
	cap.Flag("api-key", "Carina API Key [CARINA_APIKEY/RS_API_KEY]").StringVar(&ctx.APIKey)
	cap.Flag("password", "Rackspace Password [OS_PASSWORD]").StringVar(&ctx.Password)
	cap.Flag("project", "Rackspace Project Name [OS_PROJECT_NAME]").StringVar(&ctx.Project)
	cap.Flag("domain", "Rackspace Domain Name [OS_DOMAIN_NAME]").StringVar(&ctx.Domain)
	cap.Flag("region", "Rackspace Region Name [OS_REGION_NAME]").StringVar(&ctx.Region)
	cap.Flag("endpoint", "Carina API endpoint [OS_AUTH_URL]").StringVar(&ctx.Endpoint)
	cap.Flag("cache", "Cache API tokens and update times; defaults to true, use --no-cache to turn off").Default("true").BoolVar(&ctx.CacheEnabled)

	cap.PreAction(cap.initCache)
	cap.PreAction(cap.informLatest)

	writer := new(tabwriter.Writer)
	writer.Init(os.Stdout, 20, 1, 3, ' ', 0)

	// Make sure the tabwriter gets flushed at the end
	app.Terminate(func(code int) {
		// Squish any errors from flush, since we're terminating the app anyway
		_ = ctx.TabWriter.Flush()
		os.Exit(code)
	})

	cap.Flag("bash-completion", "Generate bash completion").Action(cap.generateBashCompletion).Hidden().Bool()

	ctx.TabWriter = writer

	createCommand := new(CreateCommand)
	createCommand.WaitClusterCommand = cap.NewWaitClusterCommand(ctx, "create", "Create a swarm cluster")
	createCommand.Flag("nodes", "number of nodes for the initial cluster").Default("1").IntVar(&createCommand.Nodes)
	createCommand.Flag("segments", "number of nodes for the initial cluster").Default("1").Hidden().IntVar(&createCommand.Nodes)
	createCommand.Flag("autoscale", "whether autoscale is on or off").BoolVar(&createCommand.AutoScale)
	createCommand.Action(createCommand.Create)

	getCommand := cap.NewWaitClusterCommand(ctx, "get", "Get information about a swarm cluster")
	getCommand.Action(getCommand.Get)

	inspectCommand := cap.NewWaitClusterCommand(ctx, "inspect", "Get information about a swarm cluster")
	inspectCommand.Action(inspectCommand.Get).Hidden()

	lsCommand := cap.NewCommand(ctx, "ls", "List clusters")
	lsCommand.Action(lsCommand.List)

	listCommand := cap.NewCommand(ctx, "list", "List clusters")
	listCommand.Action(listCommand.List).Hidden()

	growCommand := new(GrowCommand)
	growCommand.ClusterCommand = cap.NewClusterCommand(ctx, "grow", "Grow a cluster by the requested number of nodes")
	growCommand.Flag("by", "number of nodes to increase the cluster by").Required().IntVar(&growCommand.Nodes)
	growCommand.Action(growCommand.Grow)

	autoscaleCommand := new(AutoScaleCommand)
	autoscaleCommand.ClusterCommand = cap.NewClusterCommand(ctx, "autoscale", "Enable or disable autoscale on a cluster")
	autoscaleCommand.Arg("autoscale", "whether autoscale is on or off").EnumVar(&autoscaleCommand.AutoScale, AutoScaleOn, AutoScaleOff)
	autoscaleCommand.Action(autoscaleCommand.SetAutoScale)

	credentialsCommand := cap.NewCredentialsCommand(ctx, "credentials", "download credentials")
	credentialsCommand.Action(credentialsCommand.Download)

	// Hidden shorthand
	credsCommand := cap.NewCredentialsCommand(ctx, "creds", "download credentials")
	credsCommand.Action(credsCommand.Download).Hidden()

	envCommand := cap.NewEnvCommand(ctx, "env", "show source command for setting credential environment")
	envCommand.Action(envCommand.Show)

	rebuildCommand := cap.NewWaitClusterCommand(ctx, "rebuild", "Rebuild a swarm cluster")
	rebuildCommand.Action(rebuildCommand.Rebuild)

	rmCommand := cap.NewCredentialsCommand(ctx, "rm", "Remove a swarm cluster")
	rmCommand.Action(rmCommand.Delete)

	deleteCommand := cap.NewCredentialsCommand(ctx, "delete", "Delete a swarm cluster")
	deleteCommand.Action(deleteCommand.Delete).Hidden()

	quotasCommand := cap.NewCommand(ctx, "quotas", "Get user quotas")
	quotasCommand.Action(quotasCommand.Quotas)

	return cap
}

// VersionString returns the current version and commit of this binary (if set)
func VersionString() string {
	s := ""
	s += fmt.Sprintf("Version: %s\n", version.Version)
	s += fmt.Sprintf("Commit:  %s", version.Commit)
	return s
}

// InitCache sets up the cache for carina
func (app *Application) initCache(pc *kingpin.ParseContext) error {
	if app.CacheEnabled {
		bd, err := CarinaCredentialsBaseDir()
		if err != nil {
			return err
		}
		err = os.MkdirAll(bd, 0777)
		if err != nil {
			return err
		}

		cacheName, err := defaultCacheFilename()
		if err != nil {
			return err
		}
		app.Cache, err = LoadCache(cacheName)
		return err
	}
	return nil
}

// NewCommand creates a command wrapped with carina.Context
func (app *Application) NewCommand(ctx *Context, name, help string) *Command {
	cmd := new(Command)
	cmd.Context = ctx
	cmd.CmdClause = app.Command(name, help)
	cmd.PreAction(cmd.initFlags)
	cmd.Flag("cloud", "The cloud type: magnum, make-swarm or make-coe. This is automatically detected using the provided credentials.").Hidden().StringVar(&cmd.CloudType)
	return cmd
}

// NewClusterCommand is a command that uses a cluster name
func (app *Application) NewClusterCommand(ctx *Context, name, help string) *ClusterCommand {
	cc := new(ClusterCommand)
	cc.Command = app.NewCommand(ctx, name, help)
	cc.Arg("cluster-name", "name of the cluster").Required().StringVar(&cc.ClusterName)
	cc.PreAction(cc.Auth)
	return cc
}

// NewCredentialsCommand is a command that dumps out credentials to a path
func (app *Application) NewCredentialsCommand(ctx *Context, name, help string) *CredentialsCommand {
	credentialsCommand := new(CredentialsCommand)
	credentialsCommand.ClusterCommand = app.NewClusterCommand(ctx, name, help)
	credentialsCommand.Flag("path", "path to read & write credentials").PlaceHolder("<cluster-name>").StringVar(&credentialsCommand.Path)
	credentialsCommand.Flag("silent", "Do not print to stdout").Hidden().BoolVar(&credentialsCommand.Silent)
	return credentialsCommand
}

// NewEnvCommand initializes a `carina env` command
func (app *Application) NewEnvCommand(ctx *Context, name, help string) *ShellCommand {
	envCommand := new(ShellCommand)
	envCommand.CredentialsCommand = app.NewCredentialsCommand(ctx, name, help)
	envCommand.Flag("shell", "Force environment to be configured for specified shell").StringVar(&envCommand.Shell)
	return envCommand
}

// NewWaitClusterCommand is a command that uses a cluster name and allows the
// user to wait for a cluster state
func (app *Application) NewWaitClusterCommand(ctx *Context, name, help string) *WaitClusterCommand {
	wcc := new(WaitClusterCommand)
	wcc.ClusterCommand = app.NewClusterCommand(ctx, name, help)
	wcc.Flag("wait", "wait for swarm cluster to come online (or error)").BoolVar(&wcc.Wait)
	return wcc
}

type semver struct {
	Major    int
	Minor    int
	Patch    int
	Leftover string
}

func extractSemver(semi string) (*semver, error) {
	var err error

	if len(semi) < 5 { // 1.3.5
		return nil, errors.New("Invalid semver")
	}
	// Allow a v in front
	if semi[0] == 'v' {
		semi = semi[1:]
	}
	semVerStrings := strings.SplitN(semi, ".", 3)

	if len(semVerStrings) < 3 {
		return nil, errors.New("Could not parse semver")
	}

	parsedSemver := new(semver)

	digitError := errors.New("Could not parse digits of semver")
	if parsedSemver.Major, err = strconv.Atoi(semVerStrings[0]); err != nil {
		return nil, digitError
	}
	if parsedSemver.Minor, err = strconv.Atoi(semVerStrings[1]); err != nil {
		return nil, digitError
	}

	var ps []rune

	// Now to extract the patch and any follow on
	for i, char := range semVerStrings[2] {
		if !unicode.IsDigit(char) {
			parsedSemver.Leftover = semVerStrings[2][i:]
			break
		}
		ps = append(ps, char)
	}

	if parsedSemver.Patch, err = strconv.Atoi(string(ps)); err != nil {
		return nil, digitError
	}

	return parsedSemver, nil

}

func (s *semver) Greater(s2 *semver) bool {
	switch {
	case s.Major == s2.Major && s.Minor == s2.Minor:
		return s.Patch > s2.Patch
	case s.Major == s2.Major:
		return s.Minor > s2.Minor
	}

	return s.Major > s2.Major
}

func (s *semver) String() string {
	return fmt.Sprintf("%d.%d.%d", s.Major, s.Minor, s.Patch)
}

func (carina *Application) shouldCheckForUpdate() (bool, error) {
	lastCheck := carina.Cache.LastUpdateCheck

	// If we last checked `delay` ago, don't check again
	if lastCheck.Add(12 * time.Hour).After(time.Now()) {
		return false, nil
	}

	err := carina.Cache.UpdateLastCheck(time.Now())

	if err != nil {
		return false, err
	}

	if strings.Contains(version.Version, "-dev") || version.Version == "" {
		fmt.Fprintln(os.Stderr, "# [WARN] In dev mode, not checking for latest release")
		return false, nil
	}

	return true, nil
}

func (carina *Application) informLatest(pc *kingpin.ParseContext) error {
	if !carina.CacheEnabled {
		return nil
	}

	ok, err := carina.shouldCheckForUpdate()
	if !ok {
		return err
	}

	rel, err := version.LatestRelease()
	if err != nil {
		fmt.Fprintf(os.Stderr, "# [WARN] Unable to fetch information about the latest release of %s. %s\n.", os.Args[0], err)
		return nil
	}

	latest, err := extractSemver(rel.TagName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "# [WARN] Trouble parsing latest tag (%v): %s\n", rel.TagName, err)
		return nil
	}
	current, err := extractSemver(version.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "# [WARN] Trouble parsing current tag (%v): %s\n", version.Version, err)
		return nil
	}

	if latest.Greater(current) {
		fmt.Fprintf(os.Stderr, "# A new version of the Carina client is out, go get it\n")
		fmt.Fprintf(os.Stderr, "# You're on %v and the latest is %v\n", current, latest)
		fmt.Fprintf(os.Stderr, "# https://github.com/getcarina/carina/releases\n")
	}

	return nil
}

const httpTimeout = time.Second * 15
const cloudMakeSwarm = "make-swarm"
const cloudMakeCOE = "make-coe"
const cloudMagnum = "magnum"

func (cmd *Command) initFlags(pc *kingpin.ParseContext) error {
	// Require either an apikey or password
	apikeyFound := cmd.APIKey != "" || os.Getenv(CarinaAPIKeyEnvVar) != "" || os.Getenv(RackspaceAPIKeyEnvVar) != ""
	passwordFound := cmd.Password != "" || os.Getenv(OpenStackPasswordEnvVar) != ""
	if !apikeyFound && !passwordFound {
		return errors.New("No credentials provided. An --apikey or --password must either be specified or the equivalent environment variables must be set. Run carina --help for more information.")
	}

	if cmd.CloudType == "" {
		fmt.Println("[DEBUG] No cloud type specified, detecting with the provided credentials. Use --cloud=[magnum|make-coe|make-swarm] to skip detection.")
		if apikeyFound {
			cmd.CloudType = cloudMakeSwarm
			fmt.Println("[DEBUG] Cloud: make-swarm")
		} else {
			cmd.CloudType = cloudMagnum
			fmt.Println("[DEBUG] Cloud: Magnum")
		}
	}

	if cmd.CloudType == cloudMakeSwarm || cmd.CloudType == cloudMakeCOE {
		return initCarinaFlags(cmd)
	}

	if cmd.CloudType == cloudMagnum {
		return initMagnumFlags(cmd)
	}

	return nil
}

func initCarinaFlags(cmd *Command) error {
	// endpoint = --endpoint -> public carina endpoint
	if cmd.Endpoint == "" {
		cmd.Endpoint = libcarina.BetaEndpoint
		fmt.Printf("[DEBUG] Endpoint: %s\n", libcarina.BetaEndpoint)
	} else {
		fmt.Println("[DEBUG] Endpoint: --endpoint")
	}

	// username = --username -> CARINA_USERNAME -> RS_USERNAME
	if cmd.Username == "" {
		cmd.Username = os.Getenv(CarinaUserNameEnvVar)
		if cmd.Username == "" {
			cmd.Username = os.Getenv(RackspaceUserNameEnvVar)
			if cmd.Username == "" {
				return errors.New(fmt.Sprintf("UserName was not specified. Either use --username or set %s, or %s.\n", CarinaUserNameEnvVar, RackspaceUserNameEnvVar))
			} else {
				fmt.Printf("[DEBUG] UserName: %s\n", RackspaceUserNameEnvVar)
			}
		} else {
			fmt.Printf("[DEBUG] UserName: %s\n", CarinaUserNameEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] UserName: --username")
	}

	// api-key = --api-key -> CARINA_APIKEY -> RS_API_KEY
	if cmd.APIKey == "" {
		cmd.APIKey = os.Getenv(CarinaAPIKeyEnvVar)
		if cmd.APIKey == "" {
			cmd.APIKey = os.Getenv(RackspaceAPIKeyEnvVar)
			if cmd.APIKey == "" {
				return errors.New(fmt.Sprintf("API Key was not specified. Either use --api-key or set %s or %s.\n", CarinaAPIKeyEnvVar, RackspaceAPIKeyEnvVar))
			} else {
				fmt.Printf("[DEBUG] API Key: %s\n", RackspaceAPIKeyEnvVar)
			}
		} else {
			fmt.Printf("[DEBUG] API Key: %s\n", CarinaAPIKeyEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] API Key: --api-key")
	}

	return nil
}

func initMagnumFlags(cmd *Command) error {
	if cmd.Endpoint == "" {
		cmd.Endpoint = os.Getenv(OpenStackAuthURLEnvVar)
		if cmd.Endpoint == "" {
			return errors.New(fmt.Sprintf("Endpoint was not specified via --endpoint or %s", OpenStackAuthURLEnvVar))
		} else {
			fmt.Printf("[DEBUG] Endpoint: %s\n", OpenStackAuthURLEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] Endpoint: --endpoint")
	}

	// username = --username -> if magnum OS_PASSWORD; else CARINA_USERNAME -> RACKSPACE USERNAME
	if cmd.Username == "" {
		if cmd.CloudType == cloudMagnum {
			cmd.Username = os.Getenv(OpenStackUserNameEnvVar)
			if cmd.Username == "" {
				return errors.New(fmt.Sprintf("UserName was not specified via --username or %s", OpenStackUserNameEnvVar))
			} else {
				fmt.Printf("[DEBUG] UserName: %s\n", OpenStackUserNameEnvVar)
			}
		} else {
			cmd.Username = os.Getenv(CarinaUserNameEnvVar)
			if cmd.Username == "" {
				cmd.Username = os.Getenv(RackspaceUserNameEnvVar)
				if cmd.Username == "" {
					return errors.New(fmt.Sprintf("UserName was not specified via --username, %s or %s.", CarinaUserNameEnvVar, RackspaceUserNameEnvVar))
				} else {
					fmt.Printf("[DEBUG] UserName: %s\n", RackspaceUserNameEnvVar)
				}
			} else {
				fmt.Printf("[DEBUG] UserName: %s\n", CarinaUserNameEnvVar)
			}
		}

	} else {
		fmt.Println("[DEBUG] UserName: --username")
	}

	if cmd.Password == "" {
		cmd.Password = os.Getenv(OpenStackPasswordEnvVar)
		if cmd.Password == "" {
			return errors.New(fmt.Sprintf("Password was not specified via --password or %s", OpenStackPasswordEnvVar))
		} else {
			fmt.Printf("[DEBUG] Password: %s\n", OpenStackPasswordEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] Password: --password")
	}

	if cmd.Project == "" {
		cmd.Project = os.Getenv(OpenStackProjectEnvVar)
		if cmd.Project == "" {
			fmt.Printf("[DEBUG] Project was not specified. Either use --project or set %s.\n", OpenStackProjectEnvVar)
		} else {
			fmt.Printf("[DEBUG] Project: %s\n", OpenStackProjectEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] Project: --project")
	}

	if cmd.Domain == "" {
		cmd.Domain = os.Getenv(OpenStackDomainEnvVar)
		if cmd.Domain == "" {
			cmd.Domain = "default"
			fmt.Printf("[DEBUG] Domain: default. Either use --domain or set %s.\n", OpenStackDomainEnvVar)
		} else {
			fmt.Printf("[DEBUG] Domain: %s\n", OpenStackDomainEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] Domain: --domain")
	}

	if cmd.Region == "" {
		cmd.Region = os.Getenv(OpenStackRegionEnvVar)
		if cmd.Region == "" {
			fmt.Printf("[DEBUG] Region was not specified. Either use --region or set %s.\n", OpenStackRegionEnvVar)
		} else {
			fmt.Printf("[DEBUG] Region: %s\n", OpenStackRegionEnvVar)
		}
	} else {
		fmt.Println("[DEBUG] Region: --region")
	}

	return nil
}

// Auth does the authentication
func (carina *Command) Auth(pc *kingpin.ParseContext) (err error) {

	// Short circuit if the cache is not enabled
	if !carina.CacheEnabled {
		carina.ClusterClient, err = libcarina.NewClusterClient(carina.Endpoint, carina.Username, carina.APIKey)
		if err != nil {
			carina.ClusterClient.Client.Timeout = httpTimeout
		}
		return err
	}

	token, ok := carina.Cache.Tokens[carina.Username]

	if ok {
		carina.ClusterClient = &libcarina.ClusterClient{
			Client:   &http.Client{Timeout: httpTimeout},
			Username: carina.Username,
			Token:    token,
			Endpoint: carina.Endpoint,
		}

		if dummyRequest(carina.ClusterClient) == nil {
			return nil
		}
		// Otherwise we fall through and authenticate again
	}

	carina.ClusterClient, err = libcarina.NewClusterClient(carina.Endpoint, carina.Username, carina.APIKey)
	if err != nil {
		return err
	}
	err = carina.Cache.SetToken(carina.Username, carina.ClusterClient.Token)
	return err
}

func dummyRequest(c *libcarina.ClusterClient) error {
	req, err := http.NewRequest("HEAD", c.Endpoint+"/clusters/"+c.Username, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "getcarina/carina dummy request")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("X-Auth-Token", c.Token)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("Unable to auth on %s", "/clusters"+c.Username)
	}

	return nil
}

func (cmd *Command) getAdapter() (adapter adapters.Adapter, err error) {
	var credentials adapters.UserCredentials

	switch cmd.CloudType {
	case cloudMakeSwarm:
		adapter = &adapters.MakeSwarm{Output: cmd.TabWriter}
		credentials = adapters.UserCredentials{Endpoint: cmd.Endpoint, UserName: cmd.Username, Secret: cmd.APIKey}
	case cloudMagnum:
		adapter = &adapters.Magnum{Output: cmd.TabWriter}
		credentials = adapters.UserCredentials{Endpoint: cmd.Endpoint, UserName: cmd.Username, Secret: cmd.Password, Project: cmd.Project, Domain: cmd.Domain}
	}

	err = adapter.LoadCredentials(credentials)
	return
}

// List displays attributes for all clusters
func (cmd *Command) List(pc *kingpin.ParseContext) (err error) {
	adapter, err := cmd.getAdapter()
	return adapter.ListClusters()
}

type clusterOp func(clusterName string) (*libcarina.Cluster, error)

// Does an func against a cluster then returns the new cluster representation
func (carina *ClusterCommand) clusterApply(op clusterOp) (err error) {
	cluster, err := op(carina.ClusterName)
	if err != nil {
		return err
	}

	writeClusterHeader(carina.TabWriter)
	err = writeCluster(carina.TabWriter, cluster)
	if err != nil {
		return err
	}
	return carina.TabWriter.Flush()
}

// Get displays attributes of an individual cluster
func (cmd *WaitClusterCommand) Get(pc *kingpin.ParseContext) (err error) {
	adapter, err := cmd.getAdapter()
	return adapter.ShowCluster(cmd.ClusterName)
}

// Delete a cluster
func (carina *CredentialsCommand) Delete(pc *kingpin.ParseContext) (err error) {
	err = carina.clusterApply(carina.ClusterClient.Delete)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Unable to delete cluster, not deleting credentials on disk")
		return err
	}
	p, err := carina.clusterPath()

	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to locate carina config path, not deleteing credentials on disk\n")
		return err
	}

	p = filepath.Clean(p)
	if p == "" || p == "." || p == "/" {
		return errors.New("Path to cluster is empty, the current directory, or a root path, not deleting")
	}

	_, statErr := os.Stat(p)
	if os.IsNotExist(statErr) {
		// Assume credentials were never on disk
		return nil
	}

	// If the path exists but not the actual credentials, inform user
	_, statErr = os.Stat(filepath.Join(p, "ca.pem"))
	if os.IsNotExist(statErr) {
		return errors.New("Path to cluster credentials exists but not the ca.pem, not deleting. Remove by hand.")
	}

	err = os.RemoveAll(p)
	return err
}

// Grow increases the size of the given cluster
func (carina *GrowCommand) Grow(pc *kingpin.ParseContext) (err error) {
	return carina.clusterApply(func(clusterName string) (*libcarina.Cluster, error) {
		return carina.ClusterClient.Grow(clusterName, carina.Nodes)
	})
}

// SetAutoScale sets AutoScale on the cluster
func (carina *AutoScaleCommand) SetAutoScale(pc *kingpin.ParseContext) (err error) {
	return carina.clusterApply(func(clusterName string) (*libcarina.Cluster, error) {
		scale := true

		switch carina.AutoScale {
		case AutoScaleOn:
			scale = true
			break
		case AutoScaleOff:
			scale = false
			break
		}
		return carina.ClusterClient.SetAutoScale(clusterName, scale)
	})
}

// Rebuild nukes your cluster and builds it over again
func (carina *WaitClusterCommand) Rebuild(pc *kingpin.ParseContext) (err error) {
	return carina.clusterApplyWait(carina.ClusterClient.Rebuild, true)
}

const startupFudgeFactor = 40 * time.Second
const waitBetween = 10 * time.Second

// Cluster status when new
const StatusNew = "new"

// Cluster status when building
const StatusBuilding = "building"

// Cluster status when rebuilding swarm
const StatusRebuildingSwarm = "rebuilding-swarm"

// Does an func against a cluster then returns the new cluster representation
func (carina *WaitClusterCommand) clusterApplyWait(op clusterOp, waitFirst bool) (err error) {
	if carina.Wait && waitFirst {
		time.Sleep(startupFudgeFactor)
	}

	cluster, err := op(carina.ClusterName)
	if err != nil {
		return err
	}

	if carina.Wait {
		carina.ClusterClient.Client = &http.Client{Timeout: httpTimeout}
		cluster, err = carina.ClusterClient.Get(carina.ClusterName)
		if err != nil {
			return err
		}

		status := cluster.Status

		// Transitions past point of "new" or "building" are assumed to be states we
		// can stop on.
		for status == StatusNew || status == StatusBuilding || status == StatusRebuildingSwarm {
			time.Sleep(waitBetween)
			// Assume go has held this connection live long enough
			carina.ClusterClient.Client = &http.Client{Timeout: httpTimeout}
			cluster, err = carina.ClusterClient.Get(carina.ClusterName)
			if err != nil || cluster == nil {
				// Assume we should reauth
				if err != nil {
					break
				}
				continue
			}
			status = cluster.Status
		}
	}

	if err != nil {
		return err
	}

	writeClusterHeader(carina.TabWriter)
	err = writeCluster(carina.TabWriter, cluster)
	if err != nil {
		return err
	}
	return carina.TabWriter.Flush()
}

// CredentialsBaseDirEnvVar environment variable name for where credentials are downloaded to by default
const CredentialsBaseDirEnvVar = "CARINA_CREDENTIALS_DIR"

// CarinaHomeDirEnvVar is the environment variable name for carina data, config, etc.
const CarinaHomeDirEnvVar = "CARINA_HOME"

// Create a cluster
func (carina *CreateCommand) Create(pc *kingpin.ParseContext) (err error) {
	return carina.clusterApplyWait(func(clusterName string) (*libcarina.Cluster, error) {
		if carina.Nodes < 1 {
			return nil, errors.New("nodes must be >= 1")
		}
		nodes := libcarina.Number(carina.Nodes)

		c := libcarina.Cluster{
			ClusterName: carina.ClusterName,
			Nodes:       nodes,
			AutoScale:   carina.AutoScale,
		}
		cluster, err := carina.ClusterClient.Create(c)
		return cluster, err
	}, true)
}

func (carina *CredentialsCommand) clusterPath() (p string, err error) {
	if carina.Path == "" {
		var baseDir string
		baseDir, err = CarinaCredentialsBaseDir()
		if err != nil {
			return "", err
		}
		carina.Path = filepath.Join(baseDir, clusterDirName, carina.Username, carina.ClusterName)
	}

	p = filepath.Clean(carina.Path)
	return p, err
}

const clusterDirName = "clusters"

// Download credentials for a cluster
func (carina *CredentialsCommand) Download(pc *kingpin.ParseContext) (err error) {
	credentials, err := carina.ClusterClient.GetCredentials(carina.ClusterName)
	if err != nil {
		return err
	}

	p, err := carina.clusterPath()

	if p != "." {
		err = os.MkdirAll(p, 0777)
	}

	if err != nil {
		return err
	}

	err = writeCredentials(carina.TabWriter, credentials, p)
	if err != nil {
		return err
	}

	if !carina.Silent {
		fmt.Println("#")
		fmt.Printf("# Credentials written to \"%s\"\n", p)
		fmt.Print(credentialsNextStepsString(carina.ClusterName))
		fmt.Println("#")
	}

	err = carina.TabWriter.Flush()
	return err
}

// Show the user's quotas
func (carina *Command) Quotas(pc *kingpin.ParseContext) (err error) {
	quotas, err := carina.ClusterClient.GetQuotas()
	if err != nil {
		return err
	}
	MaxClusters := strconv.FormatInt(quotas.MaxClusters.Int64(), 10)
	MaxNodesPerCluster := strconv.FormatInt(quotas.MaxNodesPerCluster.Int64(), 10)
	err = writeRow(carina.TabWriter, []string{"MaxClusters", "MaxNodesPerCluster"})
	if err != nil {
		return err
	}
	err = writeRow(carina.TabWriter, []string{MaxClusters, MaxNodesPerCluster})
	if err != nil {
		return err
	}
	err = carina.TabWriter.Flush()
	return err
}

func writeCredentials(w *tabwriter.Writer, creds *libcarina.Credentials, pth string) (err error) {
	// TODO: Prompt when file already exists?
	for fname, b := range creds.Files {
		p := filepath.Join(pth, fname)
		err = ioutil.WriteFile(p, b, 0600)
		if err != nil {
			return err
		}
	}

	return nil
}

func verifyCredentials(path string) error {
	ca, err := ioutil.ReadFile(filepath.Join(path, "ca.pem"))
	if err != nil {
		return err
	}
	caKey, err := ioutil.ReadFile(filepath.Join(path, "ca-key.pem"))
	if err != nil {
		return err
	}
	dockerEnv, err := ioutil.ReadFile(filepath.Join(path, "docker.env"))
	if err != nil {
		return err
	}
	cert, err := ioutil.ReadFile(filepath.Join(path, "cert.pem"))
	if err != nil {
		return err
	}
	key, err := ioutil.ReadFile(filepath.Join(path, "key.pem"))
	if err != nil {
		return err
	}

	creds := libcarina.Credentials{
		CA:        ca,
		CAKey:     caKey,
		DockerEnv: dockerEnv,
		Cert:      cert,
		Key:       key,
	}

	tlsConfig, err := creds.GetTLSConfig()
	if err != nil {
		return err
	}

	sourceLines := strings.Split(string(creds.DockerEnv), "\n")
	for _, line := range sourceLines {
		if strings.Index(line, "export ") == 0 {
			varDecl := strings.TrimRight(line[7:], "\n")
			eqLocation := strings.Index(varDecl, "=")

			varName := varDecl[:eqLocation]
			varValue := varDecl[eqLocation+1:]

			switch varName {
			case "DOCKER_HOST":
				creds.DockerHost = varValue
			}

		}
	}

	u, err := url.Parse(creds.DockerHost)
	if err != nil {
		return err
	}

	conn, err := tls.Dial("tcp", u.Host, tlsConfig)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// Show echos the source command, for eval `carina env <name>`
func (carina *ShellCommand) Show(pc *kingpin.ParseContext) error {
	if carina.Path == "" {
		baseDir, err := CarinaCredentialsBaseDir()
		if err != nil {
			return err
		}
		carina.Path = filepath.Join(baseDir, clusterDirName, carina.Username, carina.ClusterName)
	}

	envPath := getCredentialFilePath(carina.Path, carina.Shell)
	_, err := os.Stat(envPath)

	// Either the credentials aren't there or they're wrong
	if os.IsNotExist(err) || verifyCredentials(carina.Path) != nil {
		// Show is a NoAuth command, so we'll auth first for a download
		err := carina.Auth(pc)
		if err != nil {
			return err
		}
		carina.Silent = true // hack to force `carina credentials` to be quiet when called from `carina env`
		err = carina.Download(pc)
		if err != nil {
			return err
		}
	}

	fmt.Fprintln(os.Stdout, sourceHelpString(envPath, carina.ClusterName, carina.Shell))

	err = carina.TabWriter.Flush()
	return err
}

func writeCluster(w *tabwriter.Writer, cluster *libcarina.Cluster) (err error) {
	fields := []string{
		cluster.ClusterName,
		cluster.Flavor,
		strconv.FormatInt(cluster.Nodes.Int64(), 10),
		strconv.FormatBool(cluster.AutoScale),
		cluster.Status,
	}
	return writeRow(w, fields)
}

func writeClusterHeader(w *tabwriter.Writer) (err error) {
	headerFields := []string{
		"ClusterName",
		"Flavor",
		"Nodes",
		"AutoScale",
		"Status",
	}
	return writeRow(w, headerFields)
}

func writeRow(w *tabwriter.Writer, fields []string) (err error) {
	s := strings.Join(fields, "\t")
	_, err = w.Write([]byte(s + "\n"))
	return err
}

func (app *Application) generateBashCompletion(c *kingpin.ParseContext) error {
	app.Writer(os.Stdout)
	if err := app.UsageForContextWithTemplate(c, 2, BashCompletionTemplate); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

func main() {
	app := New()
	kingpin.MustParse(app.Parse(os.Args[1:]))
}
