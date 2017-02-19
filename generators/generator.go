package generators

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"

	"github.com/pkg/errors"
	"github.com/sabhiram/go-git-ignore"
	"github.com/yext/edward/services"
)

type Generator interface {
	Name() string
	StartWalk(basePath string)
	StopWalk()
	VisitDir(path string) (bool, error)
	Err() error
	SetErr(err error)
}

type ServiceGenerator interface {
	Services() []*services.ServiceConfig
}

type GroupGenerator interface {
	Groups() []*services.ServiceGroupConfig
}

type ImportGenerator interface {
	Imports() []string
}

type generatorBase struct {
	err      error
	basePath string
}

func (e *generatorBase) Err() error {
	return e.err
}

func (e *generatorBase) SetErr(err error) {
	e.err = err
}

func (b *generatorBase) StartWalk(basePath string) {
	b.err = nil
	b.basePath = basePath
}

// directory represents a directory for the purposes of scanning for projects
// to import.
type directory struct {
	Path     string
	Parent   *directory
	children []*directory
	ignores  *ignore.GitIgnore
}

func NewDirectory(path string, parent *directory) (*directory, error) {
	if parent != nil && parent.Ignores() != nil && parent.Ignores().MatchesPath(path) {
		return nil, nil
	}

	ignores, err := loadIgnores(path, nil)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	files, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	d := &directory{
		Path:    path,
		Parent:  parent,
		ignores: ignores,
	}

	for _, file := range files {
		if file.IsDir() {
			child, err := NewDirectory(filepath.Join(path, file.Name()), d)
			if err != nil {
				return nil, errors.WithStack(err)
			}
			d.children = append(d.children, child)
		}
	}

	return d, nil
}

// Ignores returns the .edwardignore config for this directory or any of its
// ancestor directories.
func (d *directory) Ignores() *ignore.GitIgnore {
	if d.ignores != nil {
		return d.ignores
	}

	if d.Parent != nil {
		return d.Parent.Ignores()
	}
	return nil
}

func (d *directory) Generate(generators []Generator) error {
	if d == nil || len(generators) == 0 {
		return nil
	}

	var childGenerators []Generator
	for _, generator := range generators {
		found, err := generator.VisitDir(d.Path)
		if err != nil && err != filepath.SkipDir {
			return errors.WithStack(err)
		}
		if err != filepath.SkipDir {
			childGenerators = append(childGenerators, generator)
		}
		if found {
			break
		}
	}

	for _, child := range d.children {
		err := child.Generate(childGenerators)
		if err != nil {
			return errors.WithStack(err)
		}
	}

	return nil
}

func loadIgnores(path string, currentIgnores *ignore.GitIgnore) (*ignore.GitIgnore, error) {
	ignoreFile := filepath.Join(path, ".edwardignore")
	if _, err := os.Stat(ignoreFile); err != nil {
		if os.IsNotExist(err) {
			return currentIgnores, nil
		}
		return currentIgnores, errors.WithStack(err)
	}

	ignores, err := ignore.CompileIgnoreFile(ignoreFile)
	return ignores, errors.WithStack(err)
}

type GeneratorCollection struct {
	Generators []Generator
	Path       string
	Targets    []string
}

func (g *GeneratorCollection) Generate() error {
	if info, err := os.Stat(g.Path); err != nil || !info.IsDir() {
		if err != nil {
			return errors.WithStack(err)
		}
		return errors.New(g.Path + " is not a directory")
	}

	dir, err := NewDirectory(g.Path, nil)
	if err != nil {
		return errors.WithStack(err)
	}

	for _, generator := range g.Generators {
		generator.StartWalk(g.Path)
	}
	defer func() {
		for _, generator := range g.Generators {
			generator.StopWalk()
		}
	}()

	return errors.WithStack(dir.Generate(g.Generators))
}

func (g *GeneratorCollection) Services() []*services.ServiceConfig {
	var outServices []*services.ServiceConfig
	var serviceToGenerator = make(map[string]string)

	for _, generator := range g.Generators {
		if serviceGenerator, ok := generator.(ServiceGenerator); ok && generator.Err() == nil {
			found := serviceGenerator.Services()
			for _, service := range found {
				serviceToGenerator[service.Name] = generator.Name()
			}
			outServices = append(outServices, found...)
		}
	}

	if len(g.Targets) == 0 {
		sort.Sort(ByName(outServices))
		return outServices
	}

	filterMap := make(map[string]struct{})
	for _, name := range g.Targets {
		filterMap[name] = struct{}{}
	}

	var filteredServices []*services.ServiceConfig
	for _, service := range outServices {
		if _, ok := filterMap[service.Name]; ok {
			filteredServices = append(filteredServices, service)
		}
	}
	sort.Sort(ByName(filteredServices))
	return filteredServices
}

func (g *GeneratorCollection) Groups() []*services.ServiceGroupConfig {
	var outGroups []*services.ServiceGroupConfig
	var groupToGenerator = make(map[string]string)

	for _, generator := range g.Generators {
		if groupGenerator, ok := generator.(GroupGenerator); ok && generator.Err() == nil {
			found := groupGenerator.Groups()
			for _, group := range found {
				groupToGenerator[group.Name] = generator.Name()
			}
			outGroups = append(outGroups, found...)
		}
	}

	if len(g.Targets) == 0 {
		sort.Sort(ByGroupName(outGroups))
		return outGroups
	}

	filterMap := make(map[string]struct{})
	for _, name := range g.Targets {
		filterMap[name] = struct{}{}
	}

	var filteredGroups []*services.ServiceGroupConfig
	for _, group := range outGroups {
		if _, ok := filterMap[group.Name]; ok {
			filteredGroups = append(filteredGroups, group)
		}
	}
	sort.Sort(ByGroupName(filteredGroups))
	return filteredGroups
}

func (g *GeneratorCollection) Imports() []string {
	var outImports []string
	for _, generator := range g.Generators {
		if importGenerator, ok := generator.(ImportGenerator); ok && generator.Err() == nil {
			outImports = append(outImports, importGenerator.Imports()...)
		}
	}
	return outImports
}

type ByGroupName []*services.ServiceGroupConfig

func (s ByGroupName) Len() int {
	return len(s)
}
func (s ByGroupName) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s ByGroupName) Less(i, j int) bool {
	return s[i].Name < s[j].Name
}

type ByName []*services.ServiceConfig

func (s ByName) Len() int {
	return len(s)
}
func (s ByName) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s ByName) Less(i, j int) bool {
	return s[i].Name < s[j].Name
}
