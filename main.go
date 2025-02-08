package main

import (
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

const (
	layerRoot    = "root"
	layerUnknown = "unknown"
)

// Config 定义增强版 YAML 配置结构
type Config struct {
	Layers          map[string]LayerConfig `yaml:"layers"`
	DependencyRules []DependencyRule       `yaml:"dependency_rules"`
	ExcludeDirs     []string               `yaml:"exclude_dirs"` // 新增排除目录配置
}

type LayerConfig struct {
	Paths []string `yaml:"paths"`
}

type DependencyRule struct {
	From  string `yaml:"from"`
	To    string `yaml:"to"`
	Allow bool   `yaml:"allow"`
}

type PackageInfo struct {
	Path         string
	Module       string
	Layer        string
	Imports      []string
	LayerDeps    map[string]bool // 层依赖关系
	ExternalDeps map[string]bool // 外部依赖记录
}

func main() {
	projectRoot := flag.String("project-root", "", "The root directory of the Go project")
	configPath := flag.String("config", "config.yaml", "Path to the configuration YAML file")
	flag.Parse()

	if *projectRoot == "" || *configPath == "" {
		log.Fatal("Both project-root and config must be specified")
	}

	config, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	packages, err := parseProject(*projectRoot, config)
	if err != nil {
		log.Fatalf("Failed to parse project: %v", err)
	}

	analyzePackages(packages, config)
	checkDependencies(packages, config)
}

// loadConfig 加载增强版配置
func loadConfig(configFile string) (*Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %v", err)
	}

	return &config, nil
}

// parseProject 实现目录排除和外部包检测
func parseProject(root string, config *Config) (map[string]*PackageInfo, error) {
	packages := make(map[string]*PackageInfo)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 排除目录处理
		if info.IsDir() {
			relPath, _ := filepath.Rel(root, path)
			for _, pattern := range config.ExcludeDirs {
				if match, _ := doublestar.Match(pattern, relPath); match {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}

		// 获取模块信息
		modDir, modPrefix, err := findNearestModule(path)
		if err != nil {
			return fmt.Errorf("failed to find module for %s: %v", path, err)
		}

		// 解析Go文件
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("failed to parse file %s: %v", path, err)
		}

		// 获取包相对路径
		relPath, _ := filepath.Rel(modDir, filepath.Dir(path))
		relPath = filepath.ToSlash(relPath)
		pkgPath := fmt.Sprintf("%s/%s", modPrefix, relPath)
		if _, exists := packages[pkgPath]; !exists {
			packages[pkgPath] = &PackageInfo{
				Path:         pkgPath,
				Module:       modPrefix,
				Layer:        getLayerForPackage(pkgPath, relPath, config),
				Imports:      []string{},
				LayerDeps:    make(map[string]bool),
				ExternalDeps: make(map[string]bool),
			}
		}

		// 记录导入
		for _, imp := range node.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			packages[pkgPath].Imports = append(packages[pkgPath].Imports, impPath)
		}

		return nil
	})

	return packages, err
}

// analyzePackages 完整实现层依赖分析
func analyzePackages(packages map[string]*PackageInfo, config *Config) {
	// 构建模块路径前缀集合
	modulePrefixes := make(map[string]bool)
	for _, pkg := range packages {
		modulePrefixes[pkg.Module] = true
	}

	for _, pkg := range packages {
		for _, imp := range pkg.Imports {
			// 判断是否为外部依赖
			isExternal := true
			for module := range modulePrefixes {
				if strings.HasPrefix(imp, module) {
					isExternal = false
					break
				}
			}

			if isExternal {
				pkg.ExternalDeps[imp] = true
				continue
			}

			// 查找被导入包的信息
			if impPkg, exists := packages[imp]; exists {
				pkg.LayerDeps[impPkg.Layer] = true
				if impPkg.Layer == layerUnknown {
					fmt.Printf("[warn] pkg `%s` imports UNKNOWN `%s`\n", pkg.Path, impPkg.Path)
				}
			}
		}
	}
}

// checkDependencies 增强依赖检查
func checkDependencies(packages map[string]*PackageInfo, config *Config) {
	// 检查层依赖规则
	for _, pkg := range packages {
		for layer := range pkg.LayerDeps {
			for _, rule := range config.DependencyRules {
				if matchPattern(pkg.Layer, rule.From) && matchPattern(layer, rule.To) {
					if !rule.Allow {
						fmt.Printf("LAYER VIOLATION: %s (%s) -> %s\n",
							pkg.Path, pkg.Layer, layer)
					}
					break
				}
			}
			fmt.Printf("[debug] layer deps: %s (%s) -> %s\n", pkg.Path, pkg.Layer, layer)
		}

		// 检查外部依赖规则
		for extPkg := range pkg.ExternalDeps {
			for _, rule := range config.DependencyRules {
				if matchPattern(pkg.Layer, rule.From) && matchPattern(extPkg, rule.To) {
					if !rule.Allow {
						fmt.Printf("EXTERNAL VIOLATION: %s (%s) -> %s\n",
							pkg.Path, pkg.Layer, extPkg)
					}
					break
				}
			}
			//fmt.Printf("[debug] exteranl deps: %s (%s) -> %s\n", pkg.Path, pkg.Layer, extPkg)
		}
	}
}

func getPackageInfo(path string) (string, error) {
	// 定义模块路径和当前目录
	var modulePath string
	currentDir := path

	// 向上遍历查找 go.mod 文件
	for {
		// 检查当前目录是否存在 go.mod 文件
		goModPath := filepath.Join(currentDir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			// 读取 go.mod 文件获取模块路径
			data, err := os.ReadFile(goModPath)
			if err != nil {
				return "", err
			}
			// 提取模块路径
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "module ") {
					modulePath = strings.TrimSpace(strings.TrimPrefix(line, "module "))
					break
				}
			}
			break
		}

		// 向上遍历到父目录
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			// 到达根目录，未找到 go.mod 文件
			return "", fmt.Errorf("未找到 go.mod 文件")
		}
		currentDir = parentDir
	}

	// 获取相对路径
	relativePath, err := filepath.Rel(filepath.Dir(modulePath), path)
	if err != nil {
		return "", err
	}

	// 构建完整的 package 路径
	packagePath := fmt.Sprintf("%s/%s", modulePath, filepath.ToSlash(relativePath))
	return packagePath, nil
}

// findNearestModule 递归查找最近的 go.mod 文件
func findNearestModule(filePath string) (modDir string, modPath string, err error) {
	dir := filepath.Dir(filePath)
	for {
		modPath := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(modPath); err == nil {
			modData, err := os.ReadFile(modPath)
			if err != nil {
				return "", "", fmt.Errorf("failed to read go.mod: %v", err)
			}

			modFile, err := modfile.Parse(modPath, modData, nil)
			if err != nil {
				return "", "", fmt.Errorf("failed to parse go.mod: %v", err)
			}

			return dir, modFile.Module.Mod.Path, nil
		}

		parentDir := filepath.Dir(dir)
		if parentDir == dir {
			break // 到达根目录
		}
		dir = parentDir
	}
	return "", "", fmt.Errorf("go.mod not found for %s", filePath)
}

// getLayerForPackage 根据包路径获取其所属的层
func getLayerForPackage(pkgPath string, relPath string, config *Config) string {
	pkgSlashPath := filepath.ToSlash(pkgPath)
	for layer, layerConfig := range config.Layers {
		for _, path := range layerConfig.Paths {
			if match, _ := doublestar.Match(path, pkgSlashPath); match {
				fmt.Printf("[debug] pkg `%s` is in layer `%s`\n", pkgSlashPath, layer)
				return layer
			}
		}
	}
	if relPath == "." {
		return layerRoot
	}

	fmt.Printf("[warn] pkg `%s` is in layer `UNKNOWN`\n", pkgSlashPath)
	return layerUnknown
}

// matchPattern 支持通配符匹配
func matchPattern(path, pattern string) bool {
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		return strings.HasPrefix(path, parts[0])
	}
	return path == pattern
}

// go run deepseek/main.go --project-root=../go-wild-workouts-ddd-example/ --config=deepseek/config.yaml
