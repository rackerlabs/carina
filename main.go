package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/alecthomas/kingpin.v2"

	carinaclient "github.com/getcarina/carina/client"
	"github.com/getcarina/carina/console"
	"github.com/getcarina/carina/magnum"
	"github.com/getcarina/carina/makeswarm"
	"github.com/getcarina/carina/version"
	"github.com/getcarina/libcarina"
	"github.com/pkg/errors"
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
}

// Context contains the global application flags
type Context struct {
	client       *carinaclient.Client
	CloudType    string
	Username     string
	APIKey       string
	Password     string
	Project      string
	Domain       string
	Region       string
	Endpoint     string
	CacheEnabled bool
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
	Nodes int
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

	baseDir, err := carinaclient.GetCredentialsDir()
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
	cap.Flag("cloud", "The cloud type: magnum or make-swarm. This is automatically detected using the provided credentials.").EnumVar(&cap.CloudType, carinaclient.CloudMagnum, carinaclient.CloudMakeSwarm)
	cap.Flag("cache", "Cache API tokens and update times; defaults to true, use --no-cache to turn off").Default("true").BoolVar(&ctx.CacheEnabled)

	cap.PreAction(cap.initApp)

	cap.Flag("bash-completion", "Generate bash completion").Action(cap.generateBashCompletion).Hidden().Bool()

	createCommand := new(CreateCommand)
	createCommand.WaitClusterCommand = cap.NewWaitClusterCommand(ctx, "create", "Create a swarm cluster")
	createCommand.Flag("nodes", "number of nodes for the initial cluster").Default("1").IntVar(&createCommand.Nodes)
	createCommand.Flag("segments", "number of nodes for the initial cluster").Default("1").Hidden().IntVar(&createCommand.Nodes)
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
	autoscaleCommand.Arg("autoscale", "whether the cluster's autoscaling feature is enabled; defaults to on. Allowed values are on and off").Default(AutoScaleOn).EnumVar(&autoscaleCommand.AutoScale, AutoScaleOn, AutoScaleOff)
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

// NewCommand creates a command wrapped with carina.Context
func (app *Application) NewCommand(ctx *Context, name, help string) *Command {
	cmd := new(Command)
	cmd.Context = ctx
	cmd.CmdClause = app.Command(name, help)
	cmd.PreAction(cmd.initFlags)
	return cmd
}

// NewClusterCommand is a command that uses a cluster name
func (app *Application) NewClusterCommand(ctx *Context, name, help string) *ClusterCommand {
	cc := new(ClusterCommand)
	cc.Command = app.NewCommand(ctx, name, help)
	cc.Arg("cluster-name", "name of the cluster").Required().StringVar(&cc.ClusterName)
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

func (app *Application) shouldCheckForUpdate() (bool, error) {
	lastCheck := app.client.Cache.LastUpdateCheck

	// If we last checked `delay` ago, don't check again
	if lastCheck.Add(12 * time.Hour).After(time.Now()) {
		return false, nil
	}

	err := app.client.Cache.UpdateLastCheck(time.Now())

	if err != nil {
		return false, err
	}

	if strings.Contains(version.Version, "-dev") || version.Version == "" {
		fmt.Fprintln(os.Stderr, "# [WARN] In dev mode, not checking for latest release")
		return false, nil
	}

	return true, nil
}

func (app *Application) initApp(pc *kingpin.ParseContext) error {
	app.client = carinaclient.NewClient(app.CacheEnabled)

	if !app.CacheEnabled {
		return nil
	}

	ok, err := app.shouldCheckForUpdate()
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
			cmd.CloudType = carinaclient.CloudMakeSwarm
			fmt.Println("[DEBUG] Cloud: make-swarm")
		} else {
			cmd.CloudType = carinaclient.CloudMagnum
			fmt.Println("[DEBUG] Cloud: Magnum")
		}
	}

	cmd.client = carinaclient.NewClient(cmd.CacheEnabled)

	if cmd.CloudType == carinaclient.CloudMakeSwarm || cmd.CloudType == carinaclient.CloudMakeCOE {
		return initCarinaFlags(cmd)
	}

	if cmd.CloudType == carinaclient.CloudMagnum {
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
				return fmt.Errorf("UserName was not specified. Either use --username or set %s, or %s.\n", CarinaUserNameEnvVar, RackspaceUserNameEnvVar)
			}
			fmt.Printf("[DEBUG] UserName: %s\n", RackspaceUserNameEnvVar)
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
				return fmt.Errorf("API Key was not specified. Either use --api-key or set %s or %s.\n", CarinaAPIKeyEnvVar, RackspaceAPIKeyEnvVar)
			}
			fmt.Printf("[DEBUG] API Key: %s\n", RackspaceAPIKeyEnvVar)
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
			return fmt.Errorf("Endpoint was not specified via --endpoint or %s", OpenStackAuthURLEnvVar)
		}
		fmt.Printf("[DEBUG] Endpoint: %s\n", OpenStackAuthURLEnvVar)
	} else {
		fmt.Println("[DEBUG] Endpoint: --endpoint")
	}

	// username = --username -> if magnum OS_PASSWORD; else CARINA_USERNAME -> RACKSPACE USERNAME
	if cmd.Username == "" {
		if cmd.CloudType == carinaclient.CloudMagnum {
			cmd.Username = os.Getenv(OpenStackUserNameEnvVar)
			if cmd.Username == "" {
				return fmt.Errorf("UserName was not specified via --username or %s", OpenStackUserNameEnvVar)
			}
			fmt.Printf("[DEBUG] UserName: %s\n", OpenStackUserNameEnvVar)
		} else {
			cmd.Username = os.Getenv(CarinaUserNameEnvVar)
			if cmd.Username == "" {
				cmd.Username = os.Getenv(RackspaceUserNameEnvVar)
				if cmd.Username == "" {
					return fmt.Errorf("UserName was not specified via --username, %s or %s.", CarinaUserNameEnvVar, RackspaceUserNameEnvVar)
				}
				fmt.Printf("[DEBUG] UserName: %s\n", RackspaceUserNameEnvVar)
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
			return fmt.Errorf("Password was not specified via --password or %s", OpenStackPasswordEnvVar)
		}
		fmt.Printf("[DEBUG] Password: %s\n", OpenStackPasswordEnvVar)
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

func (cmd *Command) buildAccount() *carinaclient.Account {
	account := &carinaclient.Account{CloudType: cmd.CloudType}

	switch cmd.CloudType {
	case carinaclient.CloudMakeSwarm:
		account.Credentials = makeswarm.UserCredentials{Endpoint: cmd.Endpoint, UserName: cmd.Username, APIKey: cmd.APIKey}
	case carinaclient.CloudMagnum:
		account.Credentials = magnum.MagnumCredentials{Endpoint: cmd.Endpoint, UserName: cmd.Username, Password: cmd.Password, Project: cmd.Project, Domain: cmd.Domain}
	default:
		panic(fmt.Sprintf("Unsupported cloud type: %s", cmd.CloudType))
	}

	return account
}

// List displays attributes for all clusters
func (cmd *Command) List(pc *kingpin.ParseContext) error {
	clusters, err := cmd.client.ListClusters(cmd.buildAccount())
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	for _, cluster := range clusters {
		console.WriteCluster(cluster)
	}

	return console.Err
}

// Get displays attributes of an individual cluster
func (cmd *WaitClusterCommand) Get(pc *kingpin.ParseContext) error {
	cluster, err := cmd.client.GetCluster(cmd.buildAccount(), cmd.ClusterName, cmd.Wait)
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	console.WriteCluster(cluster)

	return console.Err
}

// Delete a cluster
func (cmd *CredentialsCommand) Delete(pc *kingpin.ParseContext) error {
	cluster, err := cmd.client.DeleteCluster(cmd.buildAccount(), cmd.ClusterName)
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	console.WriteCluster(cluster)

	return console.Err
}

// Grow increases the size of the given cluster
func (cmd *GrowCommand) Grow(pc *kingpin.ParseContext) error {
	cluster, err := cmd.client.GrowCluster(cmd.buildAccount(), cmd.ClusterName, cmd.Nodes, false)
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	console.WriteCluster(cluster)

	return console.Err
}

// SetAutoScale sets AutoScale on the cluster
func (cmd *AutoScaleCommand) SetAutoScale(pc *kingpin.ParseContext) (err error) {
	isAutoScaleOn, err := strconv.ParseBool(cmd.AutoScale)
	if err != nil {
		return errors.Wrap(err, "Unable to parse the autoscale value. Allowed values are on and off")
	}

	cluster, err := cmd.client.SetAutoScale(cmd.buildAccount(), cmd.ClusterName, isAutoScaleOn)
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	console.WriteCluster(cluster)

	return console.Err
}

// Rebuild nukes your cluster and builds it over again
func (cmd *WaitClusterCommand) Rebuild(pc *kingpin.ParseContext) (err error) {
	cluster, err := cmd.client.RebuildCluster(cmd.buildAccount(), cmd.ClusterName, cmd.Wait)
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	console.WriteCluster(cluster)

	return console.Err
}

// Create a cluster
func (cmd *CreateCommand) Create(pc *kingpin.ParseContext) error {
	if cmd.Nodes < 1 {
		return errors.New("--nodes must be >= 1")
	}

	cluster, err := cmd.client.CreateCluster(cmd.buildAccount(), cmd.ClusterName, cmd.Nodes, cmd.Wait)
	if err != nil {
		return err
	}

	console.WriteClusterHeader()
	console.WriteCluster(cluster)

	return console.Err
}

// Download credentials for a cluster
func (cmd *CredentialsCommand) Download(pc *kingpin.ParseContext) error {
	credentialsPath, err := cmd.client.DownloadClusterCredentials(cmd.buildAccount(), cmd.ClusterName, cmd.Path)
	if err != nil {
		return err
	}

	if !cmd.Silent {
		fmt.Println("#")
		fmt.Printf("# Credentials written to \"%s\"\n", credentialsPath)
		fmt.Print(carinaclient.CredentialsNextStepsString(cmd.ClusterName))
		fmt.Println("#")
	}

	return nil
}

// Show the user's quotas
func (cmd *Command) Quotas(pc *kingpin.ParseContext) (err error) {
	quotas, err := cmd.client.GetQuotas(cmd.buildAccount())
	if err != nil {
		return err
	}

	console.WriteRow([]string{"MaxClusters", "MaxNodesPerCluster"})
	console.WriteRow([]string{strconv.Itoa(quotas.GetMaxClusters()), strconv.Itoa(quotas.GetMaxNodesPerCluster())})

	return console.Err
}

// Show echos the source command, for eval `carina env <name>`
func (cmd *ShellCommand) Show(pc *kingpin.ParseContext) error {
	sourceText, err := cmd.client.GetSourceCommand(cmd.buildAccount(), cmd.Shell, cmd.ClusterName, cmd.Path)
	if err != nil {
		return err
	}

	fmt.Println(sourceText)
	return nil
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
