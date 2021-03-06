package cli

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/gruntwork-io/terragrunt/aws_helper"
	"github.com/gruntwork-io/terragrunt/config"
	"github.com/gruntwork-io/terragrunt/configstack"
	"github.com/gruntwork-io/terragrunt/errors"
	"github.com/gruntwork-io/terragrunt/options"
	"github.com/gruntwork-io/terragrunt/remote"
	"github.com/gruntwork-io/terragrunt/shell"
	"github.com/gruntwork-io/terragrunt/util"
	version "github.com/hashicorp/go-version"
	"github.com/urfave/cli"
	"os"
)

const OPT_TERRAGRUNT_CONFIG = "terragrunt-config"
const OPT_TERRAGRUNT_TFPATH = "terragrunt-tfpath"
const OPT_TERRAGRUNT_NO_AUTO_INIT = "terragrunt-no-auto-init"
const OPT_NON_INTERACTIVE = "terragrunt-non-interactive"
const OPT_WORKING_DIR = "terragrunt-working-dir"
const OPT_TERRAGRUNT_SOURCE = "terragrunt-source"
const OPT_TERRAGRUNT_SOURCE_UPDATE = "terragrunt-source-update"
const OPT_TERRAGRUNT_IAM_ROLE = "terragrunt-iam-role"
const OPT_TERRAGRUNT_IGNORE_DEPENDENCY_ERRORS = "terragrunt-ignore-dependency-errors"

var ALL_TERRAGRUNT_BOOLEAN_OPTS = []string{OPT_NON_INTERACTIVE, OPT_TERRAGRUNT_SOURCE_UPDATE, OPT_TERRAGRUNT_IGNORE_DEPENDENCY_ERRORS, OPT_TERRAGRUNT_NO_AUTO_INIT}
var ALL_TERRAGRUNT_STRING_OPTS = []string{OPT_TERRAGRUNT_CONFIG, OPT_TERRAGRUNT_TFPATH, OPT_WORKING_DIR, OPT_TERRAGRUNT_SOURCE, OPT_TERRAGRUNT_IAM_ROLE}

const CMD_PLAN_ALL = "plan-all"
const CMD_APPLY_ALL = "apply-all"
const CMD_DESTROY_ALL = "destroy-all"
const CMD_OUTPUT_ALL = "output-all"
const CMD_VALIDATE_ALL = "validate-all"

const CMD_INIT = "init"

// CMD_SPIN_UP is deprecated.
const CMD_SPIN_UP = "spin-up"

// CMD_TEAR_DOWN is deprecated.
const CMD_TEAR_DOWN = "tear-down"

var MULTI_MODULE_COMMANDS = []string{CMD_APPLY_ALL, CMD_DESTROY_ALL, CMD_OUTPUT_ALL, CMD_PLAN_ALL, CMD_VALIDATE_ALL}

// DEPRECATED_COMMANDS is a map of deprecated commands to the commands that replace them.
var DEPRECATED_COMMANDS = map[string]string{
	CMD_SPIN_UP:   CMD_APPLY_ALL,
	CMD_TEAR_DOWN: CMD_DESTROY_ALL,
}

var TERRAFORM_COMMANDS_THAT_USE_STATE = []string{
	"init",
	"apply",
	"destroy",
	"env",
	"import",
	"graph",
	"output",
	"plan",
	"push",
	"refresh",
	"show",
	"taint",
	"untaint",
	"validate",
	"force-unlock",
	"state",
}

var TERRAFORM_COMMANDS_THAT_DO_NOT_NEED_INIT = []string{
	"version",
}

// Since Terragrunt is just a thin wrapper for Terraform, and we don't want to repeat every single Terraform command
// in its definition, we don't quite fit into the model of any Go CLI library. Fortunately, urfave/cli allows us to
// override the whole template used for the Usage Text.
//
// TODO: this description text has copy/pasted versions of many Terragrunt constants, such as command names and file
// names. It would be easy to make this code DRY using fmt.Sprintf(), but then it's hard to make the text align nicely.
// Write some code to take generate this help text automatically, possibly leveraging code that's part of urfave/cli.
var CUSTOM_USAGE_TEXT = `DESCRIPTION:
   {{.Name}} - {{.UsageText}}

USAGE:
   {{.Usage}}

COMMANDS:
   plan-all             Display the plans of a 'stack' by running 'terragrunt plan' in each subfolder
   apply-all            Apply a 'stack' by running 'terragrunt apply' in each subfolder
   output-all           Display the outputs of a 'stack' by running 'terragrunt output' in each subfolder
   destroy-all          Destroy a 'stack' by running 'terragrunt destroy' in each subfolder
   validate-all         Validate 'stack' by running 'terragrunt validate' in each subfolder
   *                    Terragrunt forwards all other commands directly to Terraform

GLOBAL OPTIONS:
   terragrunt-config                    Path to the Terragrunt config file. Default is terraform.tfvars.
   terragrunt-tfpath                    Path to the Terraform binary. Default is terraform (on PATH).
   terragrunt-no-auto-init              Don't automatically run 'terraform init' during other terragrunt commands. You must run 'terragrunt init' manually.
   terragrunt-non-interactive           Assume "yes" for all prompts.
   terragrunt-working-dir               The path to the Terraform templates. Default is current directory.
   terragrunt-source                    Download Terraform configurations from the specified source into a temporary folder, and run Terraform in that temporary folder.
   terragrunt-source-update             Delete the contents of the temporary folder to clear out any old, cached source code before downloading new source code into it.
   terragrunt-iam-role             		Assume the specified IAM role before executing Terraform. Can also be set via the TERRAGRUNT_IAM_ROLE environment variable.
   terragrunt-ignore-dependency-errors  *-all commands continue processing components even if a dependency fails.

VERSION:
   {{.Version}}{{if len .Authors}}

AUTHOR(S):
   {{range .Authors}}{{.}}{{end}}
   {{end}}
`

var MODULE_REGEX = regexp.MustCompile(`module[[:blank:]]+".+"`)

// This uses the constraint syntax from https://github.com/hashicorp/go-version
const DEFAULT_TERRAFORM_VERSION_CONSTRAINT = ">= v0.9.3"

const TERRAFORM_EXTENSION_GLOB = "*.tf"

// Create the Terragrunt CLI App
func CreateTerragruntCli(version string, writer io.Writer, errwriter io.Writer) *cli.App {
	cli.OsExiter = func(exitCode int) {
		// Do nothing. We just need to override this function, as the default value calls os.Exit, which
		// kills the app (or any automated test) dead in its tracks.
	}

	cli.AppHelpTemplate = CUSTOM_USAGE_TEXT

	app := cli.NewApp()

	app.Name = "terragrunt"
	app.Author = "Gruntwork <www.gruntwork.io>"
	app.Version = version
	app.Action = runApp
	app.Usage = "terragrunt <COMMAND>"
	app.Writer = writer
	app.ErrWriter = errwriter
	app.UsageText = `Terragrunt is a thin wrapper for Terraform that provides extra tools for working with multiple
   Terraform modules, remote state, and locking. For documentation, see https://github.com/gruntwork-io/terragrunt/.`

	return app
}

// The sole action for the app
func runApp(cliContext *cli.Context) (finalErr error) {
	defer errors.Recover(func(cause error) { finalErr = cause })

	// If someone calls us with no args at all, show the help text and exit
	if !cliContext.Args().Present() {
		cli.ShowAppHelp(cliContext)
		return nil
	}

	terragruntOptions, err := ParseTerragruntOptions(cliContext)
	if err != nil {
		return err
	}

	if err := PopulateTerraformVersion(terragruntOptions); err != nil {
		return err
	}

	if err := CheckTerraformVersion(DEFAULT_TERRAFORM_VERSION_CONSTRAINT, terragruntOptions); err != nil {
		return err
	}

	givenCommand := cliContext.Args().First()
	command := checkDeprecated(givenCommand, terragruntOptions)
	return runCommand(command, terragruntOptions)
}

// checkDeprecated checks if the given command is deprecated.  If so: prints a message and returns the new command.
func checkDeprecated(command string, terragruntOptions *options.TerragruntOptions) string {
	newCommand, deprecated := DEPRECATED_COMMANDS[command]
	if deprecated {
		terragruntOptions.Logger.Printf("%v is deprecated; running %v instead.\n", command, newCommand)
		return newCommand
	}
	return command
}

// runCommand runs one or many terraform commands based on the type of
// terragrunt command
func runCommand(command string, terragruntOptions *options.TerragruntOptions) (finalEff error) {
	if isMultiModuleCommand(command) {
		return runMultiModuleCommand(command, terragruntOptions)
	}
	return runTerragrunt(terragruntOptions)
}

// Downloads terraform source if necessary, then runs terraform with the given options and CLI args.
// This will forward all the args and extra_arguments directly to Terraform.
func runTerragrunt(terragruntOptions *options.TerragruntOptions) error {
	terragruntConfig, err := config.ReadTerragruntConfig(terragruntOptions)
	if err != nil {
		return err
	}

	if err := assumeRoleIfNecessary(terragruntOptions); err != nil {
		return err
	}

	if sourceUrl := getTerraformSourceUrl(terragruntOptions, terragruntConfig); sourceUrl != "" {
		if err := downloadTerraformSource(sourceUrl, terragruntOptions, terragruntConfig); err != nil {
			return err
		}
	}

	if terragruntConfig.RemoteState != nil {
		if err := checkTerraformCodeDefinesBackend(terragruntOptions, terragruntConfig.RemoteState.Backend); err != nil {
			return err
		}
	}

	return runTerragruntWithConfig(terragruntOptions, terragruntConfig, false)
}

// Assume an IAM role, if one is specified, by making API calls to Amazon STS and setting the environment variables
// we get back inside of terragruntOptions.Env
func assumeRoleIfNecessary(terragruntOptions *options.TerragruntOptions) error {
	if terragruntOptions.IamRole == "" {
		return nil
	}

	terragruntOptions.Logger.Printf("Assuming IAM role %s", terragruntOptions.IamRole)
	creds, err := aws_helper.AssumeIamRole(terragruntOptions.IamRole)
	if err != nil {
		return err
	}

	terragruntOptions.Env["AWS_ACCESS_KEY_ID"] = aws.StringValue(creds.AccessKeyId)
	terragruntOptions.Env["AWS_SECRET_ACCESS_KEY"] = aws.StringValue(creds.SecretAccessKey)
	terragruntOptions.Env["AWS_SESSION_TOKEN"] = aws.StringValue(creds.SessionToken)

	return nil
}

// Runs terraform with the given options and CLI args.
// This will forward all the args and extra_arguments directly to Terraform.
func runTerragruntWithConfig(terragruntOptions *options.TerragruntOptions, terragruntConfig *config.TerragruntConfig, allowSourceDownload bool) error {

	// Add extra_arguments to the command
	if terragruntConfig.Terraform != nil && terragruntConfig.Terraform.ExtraArgs != nil && len(terragruntConfig.Terraform.ExtraArgs) > 0 {
		terragruntOptions.InsertTerraformCliArgs(filterTerraformExtraArgs(terragruntOptions, terragruntConfig)...)
	}

	if firstArg(terragruntOptions.TerraformCliArgs) == CMD_INIT {
		if err := prepareInitCommand(terragruntOptions, terragruntConfig, allowSourceDownload); err != nil {
			return err
		}
	} else {
		if err := prepareNonInitCommand(terragruntOptions, terragruntConfig); err != nil {
			return err
		}
	}
	return shell.RunTerraformCommand(terragruntOptions, terragruntOptions.TerraformCliArgs...)
}

// Prepare for running 'terraform init' by
// preventing users from passing source download arguments,
// initializing remote state storage, and
// adding backend configuration arguments to the TerraformCliArgs
func prepareInitCommand(terragruntOptions *options.TerragruntOptions, terragruntConfig *config.TerragruntConfig, allowSourceDownload bool) error {

	// Do not allow the user to specify the source or DIR arguments
	// on the command line or as part of extra_arguments
	//
	// However, allow download_source.go to specify the source and DIR arguments.
	if err := verifySourceDownloadArguments(allowSourceDownload, terragruntOptions); err != nil {
		return err
	}

	if terragruntConfig.RemoteState != nil {
		// Initialize the remote state if necessary  (e.g. create S3 bucket and DynamoDB table)
		remoteStateNeedsInit, err := remoteStateNeedsInit(terragruntConfig.RemoteState, terragruntOptions)
		if err != nil {
			return err
		}
		if remoteStateNeedsInit {
			if err := terragruntConfig.RemoteState.Initialize(terragruntOptions); err != nil {
				return err
			}
		}

		// Add backend config arguments to the command
		terragruntOptions.InsertTerraformCliArgs(terragruntConfig.RemoteState.ToTerraformInitArgs()...)
	}
	return nil
}

// Check that the specified Terraform code defines a backend { ... } block and return an error if doesn't
func checkTerraformCodeDefinesBackend(terragruntOptions *options.TerragruntOptions, backendType string) error {
	terraformBackendRegexp, err := regexp.Compile(fmt.Sprintf(`backend[[:blank:]]+"%s"`, backendType))
	if err != nil {
		return errors.WithStackTrace(err)
	}

	definesBackend, err := util.Grep(terraformBackendRegexp, fmt.Sprintf("%s/**/*.tf", terragruntOptions.WorkingDir))
	if err != nil {
		return err
	}
	if definesBackend {
		return nil
	}

	terraformJSONBackendRegexp, err := regexp.Compile(fmt.Sprintf(`(?m)"backend":[[:space:]]*{[[:space:]]*"%s"`, backendType))
	if err != nil {
		return errors.WithStackTrace(err)
	}

	definesJSONBackend, err := util.Grep(terraformJSONBackendRegexp, fmt.Sprintf("%s/**/*.tf.json", terragruntOptions.WorkingDir))
	if err != nil {
		return err
	}
	if definesJSONBackend {
		return nil
	}

	return errors.WithStackTrace(BackendNotDefined{Opts: terragruntOptions, BackendType: backendType})
}

// Prepare for running any command other than 'terraform init' by
// running 'terraform init' if necessary
func prepareNonInitCommand(terragruntOptions *options.TerragruntOptions, terragruntConfig *config.TerragruntConfig) error {
	needsInit, err := needsInit(terragruntOptions, terragruntConfig)
	if err != nil {
		return err
	}

	if needsInit {
		if err := runTerraformInit(terragruntOptions, terragruntConfig, nil); err != nil {
			return err
		}
	}
	return nil
}

// Determines if 'terraform init' needs to be executed
func needsInit(terragruntOptions *options.TerragruntOptions, terragruntConfig *config.TerragruntConfig) (bool, error) {
	if util.ListContainsElement(TERRAFORM_COMMANDS_THAT_DO_NOT_NEED_INIT, firstArg(terragruntOptions.TerraformCliArgs)) {
		return false, nil
	}

	if providersNeedInit(terragruntOptions) {
		return true, nil
	}

	modulesNeedsInit, err := modulesNeedInit(terragruntOptions)
	if err != nil {
		return false, err
	}
	if modulesNeedsInit {
		return true, nil
	}

	return remoteStateNeedsInit(terragruntConfig.RemoteState, terragruntOptions)
}

// Returns true if we need to run `terraform init` to download providers
func providersNeedInit(terragruntOptions *options.TerragruntOptions) bool {
	providersPath := util.JoinPath(terragruntOptions.WorkingDir, ".terraform/plugins")
	return !util.FileExists(providersPath)
}

// Runs the terraform init command to perform what is referred to as Auto-Init in the README.md.
// This is intended to be run when the user runs another terragrunt command (e.g. 'terragrunt apply'),
// but terragrunt determines that 'terraform init' needs to be called pror to runing
// the respective terraform command (e.g. 'terraform apply')
//
// The terragruntOptions are assumed to be the options for running the original terragrunt command.
//
// If terraformSource is specified, then arguments to download the terraform source will be appended to the init command.
//
// This method will return an error and NOT run terraform init if the user has disabled Auto-Init
func runTerraformInit(terragruntOptions *options.TerragruntOptions, terragruntConfig *config.TerragruntConfig, terraformSource *TerraformSource) error {

	// Prevent Auto-Init if the user has disabled it
	if firstArg(terragruntOptions.TerraformCliArgs) != CMD_INIT && !terragruntOptions.AutoInit {
		return errors.WithStackTrace(InitNeededButDisabled("Cannot continue because init is needed, but Auto-Init is disabled.  You must run 'terragrunt init' manually."))
	}

	// Need to clone the terragruntOptions, so the TerraformCliArgs can be configured to run the init command
	initOptions := terragruntOptions.Clone(terragruntOptions.TerragruntConfigPath)
	initOptions.TerraformCliArgs = []string{CMD_INIT}
	initOptions.WorkingDir = terragruntOptions.WorkingDir

	// Don't pollute stdout with the stdout from Aoto Init
	initOptions.Writer = initOptions.ErrWriter

	// Only add the arguments to download source if terraformSource was specified
	downloadSource := terraformSource != nil
	if downloadSource {
		initOptions.WorkingDir = terraformSource.WorkingDir
		if !util.FileExists(terraformSource.WorkingDir) {
			if err := os.MkdirAll(terraformSource.WorkingDir, 0777); err != nil {
				return errors.WithStackTrace(err)
			}
		}

		v0_10_0, err := version.NewVersion("v0.10.0")
		if err != nil {
			return err
		}

		if terragruntOptions.TerraformVersion.LessThan(v0_10_0) {
			// Terraform versions < 0.10.0 specified the module source as an argument (rather than the -from-module option)
			initOptions.AppendTerraformCliArgs(terraformSource.CanonicalSourceURL.String())
		} else {
			// Terraform versions >= 0.10.0 specify the module source using the -from-module option
			initOptions.AppendTerraformCliArgs("-from-module=" + terraformSource.CanonicalSourceURL.String())
		}
		initOptions.AppendTerraformCliArgs(terraformSource.DownloadDir)
	}

	return runTerragruntWithConfig(initOptions, terragruntConfig, downloadSource)
}

// Returns an error if allowSourceDownload is false, and terragruntOptions.TerraformCliArgs contains source download related arguments
func verifySourceDownloadArguments(allowSourceDownload bool, terragruntOptions *options.TerragruntOptions) error {
	if allowSourceDownload || len(terragruntOptions.TerraformCliArgs) <= 1 {
		return nil
	}
	for _, arg := range terragruntOptions.TerraformCliArgs[1:] {
		// Enforce that the user did not specify -from-module
		if strings.Contains(arg, "-from-module") {
			return errors.WithStackTrace(ArgumentNotAllowed{
				Argument: arg,
				Message:  "Option not allowed: %s.  Terragrunt will handle setting -from-module automatically.",
			})
		}
		// The user is not allowed to pass non-option arguments (such as DIR)
		if !strings.HasPrefix(arg, "-") {
			return errors.WithStackTrace(ArgumentNotAllowed{
				Argument: arg,
				Message:  "Argument not allowed: %s.  Terragrunt will handle setting the module source and DIR arguments automatically.",
			})
		}
	}
	return nil
}

// Returns true if the command the user wants to execute is supposed to affect multiple Terraform modules, such as the
// apply-all or destroy-all command.
func isMultiModuleCommand(command string) bool {
	return util.ListContainsElement(MULTI_MODULE_COMMANDS, command)
}

// Execute a command that affects multiple Terraform modules, such as the apply-all or destroy-all command.
func runMultiModuleCommand(command string, terragruntOptions *options.TerragruntOptions) error {
	switch command {
	case CMD_PLAN_ALL:
		return planAll(terragruntOptions)
	case CMD_APPLY_ALL:
		return applyAll(terragruntOptions)
	case CMD_DESTROY_ALL:
		return destroyAll(terragruntOptions)
	case CMD_OUTPUT_ALL:
		return outputAll(terragruntOptions)
	case CMD_VALIDATE_ALL:
		return validateAll(terragruntOptions)
	default:
		return errors.WithStackTrace(UnrecognizedCommand(command))
	}
}

// Return true if modules aren't already downloaded and the Terraform templates in this project reference modules.
// Note that to keep the logic in this code very simple, this code ONLY detects the case where you haven't downloaded
// modules at all. Detecting if your downloaded modules are out of date (as opposed to missing entirely) is more
// complicated and not something we handle at the moment.
func modulesNeedInit(terragruntOptions *options.TerragruntOptions) (bool, error) {
	modulesPath := util.JoinPath(terragruntOptions.WorkingDir, ".terraform/modules")
	if util.FileExists(modulesPath) {
		return false, nil
	}

	return util.Grep(MODULE_REGEX, fmt.Sprintf("%s/%s", terragruntOptions.WorkingDir, TERRAFORM_EXTENSION_GLOB))
}

// If the user entered a Terraform command that uses state (e.g. plan, apply), make sure remote state is configured
// before running the command.
func remoteStateNeedsInit(remoteState *remote.RemoteState, terragruntOptions *options.TerragruntOptions) (bool, error) {

	// We only configure remote state for the commands that use the tfstate files. We do not configure it for
	// commands such as "get" or "version".
	if remoteState != nil && util.ListContainsElement(TERRAFORM_COMMANDS_THAT_USE_STATE, firstArg(terragruntOptions.TerraformCliArgs)) {
		return remoteState.NeedsInit(terragruntOptions)
	}
	return false, nil
}

// planAll prints the plans from all configuration in a stack, in the order
// specified in the terraform_remote_state dependencies
func planAll(terragruntOptions *options.TerragruntOptions) error {
	stack, err := configstack.FindStackInSubfolders(terragruntOptions)
	if err != nil {
		return err
	}

	terragruntOptions.Logger.Printf("%s", stack.String())
	return stack.Plan(terragruntOptions)
}

// Spin up an entire "stack" by running 'terragrunt apply' in each subfolder, processing them in the right order based
// on terraform_remote_state dependencies.
func applyAll(terragruntOptions *options.TerragruntOptions) error {
	stack, err := configstack.FindStackInSubfolders(terragruntOptions)
	if err != nil {
		return err
	}

	terragruntOptions.Logger.Printf("%s", stack.String())
	shouldApplyAll, err := shell.PromptUserForYesNo("Are you sure you want to run 'terragrunt apply' in each folder of the stack described above?", terragruntOptions)
	if err != nil {
		return err
	}

	if shouldApplyAll {
		return stack.Apply(terragruntOptions)
	}

	return nil
}

// Tear down an entire "stack" by running 'terragrunt destroy' in each subfolder, processing them in the right order
// based on terraform_remote_state dependencies.
func destroyAll(terragruntOptions *options.TerragruntOptions) error {
	stack, err := configstack.FindStackInSubfolders(terragruntOptions)
	if err != nil {
		return err
	}

	terragruntOptions.Logger.Printf("%s", stack.String())
	shouldDestroyAll, err := shell.PromptUserForYesNo("WARNING: Are you sure you want to run `terragrunt destroy` in each folder of the stack described above? There is no undo!", terragruntOptions)
	if err != nil {
		return err
	}

	if shouldDestroyAll {
		return stack.Destroy(terragruntOptions)
	}

	return nil
}

// outputAll prints the outputs from all configuration in a stack, in the order
// specified in the terraform_remote_state dependencies
func outputAll(terragruntOptions *options.TerragruntOptions) error {
	stack, err := configstack.FindStackInSubfolders(terragruntOptions)
	if err != nil {
		return err
	}

	terragruntOptions.Logger.Printf("%s", stack.String())
	return stack.Output(terragruntOptions)
}

// validateAll validates runs terraform validate on all the modules
func validateAll(terragruntOptions *options.TerragruntOptions) error {
	stack, err := configstack.FindStackInSubfolders(terragruntOptions)
	if err != nil {
		return err
	}

	terragruntOptions.Logger.Printf("%s", stack.String())
	return stack.Validate(terragruntOptions)
}

// Custom error types

type UnrecognizedCommand string

func (commandName UnrecognizedCommand) Error() string {
	return fmt.Sprintf("Unrecognized command: %s", string(commandName))
}

type ArgumentNotAllowed struct {
	Argument string
	Message  string
}

func (err ArgumentNotAllowed) Error() string {
	return fmt.Sprintf(err.Message, err.Argument)
}

type InitNeededButDisabled string

func (err InitNeededButDisabled) Error() string {
	return string(err)
}

type BackendNotDefined struct {
	Opts        *options.TerragruntOptions
	BackendType string
}

func (err BackendNotDefined) Error() string {
	return fmt.Sprintf("Found remote_state settings in %s but no backend block in the Terraform code in %s. You must define a backend block (it can be empty!) in your Terraform code or your remote state settings will have no effect! It should look something like this:\n\nterraform {\n  backend \"%s\" {}\n}\n\n", err.Opts.TerragruntConfigPath, err.Opts.WorkingDir, err.BackendType)
}
