package policy

import (
	"path"
	"slices"
	"strings"

	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// Secret and repository names are literal single identifiers, never lists or
// expressions. The names come from policy and action inputs, not shell text.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var prEventComparison = regexp.MustCompile(`(?i)^github\.event_name\s*==\s*'pull_request'$`)

// reviewedCheckoutDir separates every checked-out import from artifact data.
const reviewedCheckoutDir = "reviewed"

func safeRelativePath(value string) bool {
	return value != "" && pathWord.MatchString(value) && !strings.Contains(value, "..") && !strings.HasPrefix(value, "/") && path.Clean(value) != "."
}

func hasPRRunGate(cond string) bool {
	expr := stripIfExpression(cond)
	if strings.Contains(expr, "||") {
		return false
	}
	for _, part := range splitConjuncts(expr) {
		if prEventComparison.MatchString(trimOuterParens(part)) {
			return true
		}
	}
	return false
}

// actionsFor derives each class's actions from the canonical action set.
func actionsFor(class credentialJobClass) []AllowedAction {
	if !slices.Contains(allCredentialJobClasses, class) {
		return nil
	}
	var result []AllowedAction
	for _, action := range AllAllowedActions {
		if action != ActionReleaseAssets || class == credentialJobPublish {
			result = append(result, action)
		}
	}
	return result
}

func mintInputsFor(class credentialJobClass) []ActionInput {
	// The comment class explicitly requests contents: read, never write. The
	// values are checked separately; omitting all permissions inherits the App.
	if !slices.Contains(allCredentialJobClasses, class) {
		return nil
	}
	return inputsOf(ActionCreateAppToken)
}

func (c *queueChecker) hasCommentPAT() bool {
	for _, comment := range c.policy.Queue.CommentJobs {
		if comment.PATSecret != "" {
			return true
		}
	}
	return false
}

// ambiguousCredentialYAML fails closed rather than guessing which duplicate
// value or alias expansion GitHub will use when a named personal token exists.
func ambiguousCredentialYAML(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	if node.Kind == yaml.AliasNode {
		return "uses a YAML alias"
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if seen[key] {
				return "repeats YAML mapping key " + quote(key)
			}
			seen[key] = true
		}
	}
	for _, child := range node.Content {
		if why := ambiguousCredentialYAML(child); why != "" {
			return why
		}
	}
	return ""
}

func commentMintInputFault(with *yaml.Node) string {
	repos := valueOf(mappingValue(with, InputRepositories.String()))
	if !repositoryName.MatchString(repos) || strings.Contains(repos, "..") {
		return "passes repositories " + quote(repos) + "; a comment token requires exactly one literal repository"
	}
	for _, requirement := range []struct {
		input ActionInput
		level PermissionLevel
	}{
		{InputPermissionContents, PermissionRead},
		{InputPermissionIssues, PermissionWrite},
		{InputPermissionPullRequests, PermissionWrite},
	} {
		value := valueOf(mappingValue(with, requirement.input.String()))
		if value != requirement.level.String() {
			return "passes " + requirement.input.String() + ": " + quote(value) + "; the comment token requires explicit " + requirement.level.String()
		}
	}
	return ""
}

// patReads identifies named policy PATs or a whole-secrets read. It is used in
// addition to the general credential reader to confine this particular token.
func (c *queueChecker) patReads(node *yaml.Node) []string {
	var found []string
	for _, scalar := range nodeScalars(node) {
		for _, expr := range expressionBodies(scalar) {
			e := strings.ToLower(strings.Join(strings.Fields(expr), ""))
			for _, m := range contextRead.FindAllStringSubmatchIndex(e, -1) {
				if e[m[2]:m[3]] != "secrets" {
					continue
				}
				if m[4] < 0 {
					if !slices.Contains(found, "the whole secrets context") {
						found = append(found, "the whole secrets context")
					}
					continue
				}
				name := e[m[4]+1 : m[5]]
				for _, comment := range c.policy.Queue.CommentJobs {
					if comment.PATSecret != "" && strings.EqualFold(name, comment.PATSecret) && !slices.Contains(found, comment.PATSecret) {
						found = append(found, comment.PATSecret)
					}
				}
			}
		}
	}
	return found
}

// directoriesOverlap rejects either directory containing the other. The
// evidence artifact must not replace the reviewed script or its sibling
// imports, even when the artifact itself is controlled by a PR.
func directoriesOverlap(a, b string) bool {
	a, b = path.Clean(a), path.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (c *queueChecker) reviewedScriptFault(doc, job, step *yaml.Node, cj credentialJob) string {
	if cj.reviewedScript == "" || cj.patSecret == "" {
		return "the policy must name reviewed_script and pat_secret for the reviewed script"
	}
	dir := effectiveEnv(doc, job, step)[TemplateEnvEvidenceDir.String()]
	if !safeRelativePath(dir) || directoriesOverlap(dir, reviewedCheckoutDir) {
		return "EVIDENCE_DIR must be a literal directory disjoint from the reviewed checkout"
	}
	foundScript, foundDownload, checkouts := false, false, 0
	for _, candidate := range stepsOf(job) {
		if candidate == step {
			foundScript = true
		}
		uses := stepUsesText(candidate)
		if isAction(uses, ActionCheckout) {
			checkouts++
			if foundScript || valueOf(mappingValue(mappingValue(candidate, "with"), InputRef.String())) == "" ||
				soleExpression(valueOf(mappingValue(mappingValue(candidate, "with"), InputRef.String()))) != defaultBranchExpression ||
				valueOf(mappingValue(mappingValue(candidate, "with"), InputArtifactPath.String())) != reviewedCheckoutDir ||
				mappingValue(candidate, "if") != nil || hasContinueOnError(candidate) {
				return "the reviewed script requires one unconditional default-branch checkout before it runs"
			}
		}
		if isAction(uses, ActionDownloadArtifact) {
			dest := valueOf(mappingValue(mappingValue(candidate, "with"), InputArtifactPath.String()))
			if !safeRelativePath(dest) || directoriesOverlap(dest, reviewedCheckoutDir) {
				return "an artifact download can overwrite the reviewed script or its imports"
			}
			if !foundScript && path.Clean(dest) == path.Clean(dir) && mappingValue(candidate, "if") == nil && !hasContinueOnError(candidate) {
				foundDownload = true
			}
		}
	}
	if checkouts != 1 {
		return "the reviewed script requires exactly one default-branch checkout"
	}
	if !foundDownload {
		return "the reviewed script requires an unconditional earlier artifact download into EVIDENCE_DIR"
	}
	if len(statusFunctionsIn(stepIfText(step))) != 0 {
		return "the reviewed script if must not bypass failed checkout or download steps"
	}
	return ""
}
