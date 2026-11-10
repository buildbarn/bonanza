package main

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
)

type computerDefinition struct {
	Functions    map[string]functionDefinition `json:"functions"`
	GoPackage    string                        `json:"goPackage"`
	ProtoPackage string                        `json:"protoPackage"`
}

type functionDefinition struct {
	KeyContainsReferences bool
	DependsOn             []string `json:"dependsOn"`
	ObjectType            *objectTypeDefinition
	IsLookup              *bool `json:"isLookup"`
}

func getMessageName(s string) string {
	return strings.ReplaceAll(s, "HTTP", "Http")
}

func (fd functionDefinition) getKeyType(functionName string, isPatched bool) string {
	if !fd.KeyContainsReferences {
		return fmt.Sprintf("*pb.%s_Key", getMessageName(functionName))
	} else if isPatched {
		return fmt.Sprintf("model_core.PatchedMessage[*pb.%s_Key, TMetadata]", getMessageName(functionName))
	}
	return fmt.Sprintf("model_core.Message[*pb.%s_Key, TReference]", getMessageName(functionName))
}

func (fd functionDefinition) keyToPatchedMessage() string {
	if fd.KeyContainsReferences {
		return "model_core.NewPatchedMessage[proto.Message](key.Message, key.Patcher)"
	}
	return "model_core.NewSimplePatchedMessage[TMetadata](proto.Message(key))"
}

func (fd functionDefinition) typedKeyToArgument(functionName string) string {
	if fd.KeyContainsReferences {
		return fmt.Sprintf("model_core.Message[*pb.%s_Key, TReference]{Message: typedKey, OutgoingReferences: key.OutgoingReferences}", getMessageName(functionName))
	}
	return "typedKey"
}

type objectTypeDefinition struct {
	Imports map[string]string `json:"imports"`
	Type    string            `json:"type"`

	// Constraint that the type places on TReference, if any. This is
	// used to declare the type parameters of getter interfaces.
	ReferenceConstraint string `json:"referenceConstraint"`
}

// getValueGetterMethod returns the signature of the method that is
// used to obtain the value of a function.
func (fd functionDefinition) getValueGetterMethod(functionName string) string {
	return fmt.Sprintf(
		"Get%sValue(key %s) model_core.Message[*pb.%s_Value, TReference]",
		functionName,
		fd.getKeyType(functionName, true),
		getMessageName(functionName),
	)
}

// getObjectGetterMethod returns the signature of the method that is
// used to obtain the object derived from the value of a function.
func (fd functionDefinition) getObjectGetterMethod(functionName string) string {
	return fmt.Sprintf(
		"Get%sObject(key %s) (%s, bool)",
		functionName,
		fd.getKeyType(functionName, true),
		fd.ObjectType.Type,
	)
}

// getTypeParameters returns the type parameters of a getter interface
// containing a given method, both in the form used to declare the
// interface (e.g., "[TReference any]") and to instantiate it (e.g.,
// "[TReference]"). Only type parameters that are used by the method
// are included. Constraints are as liberal as possible, so that the
// interfaces can also be used by helper functions that are not specific
// to a type of reference.
func getTypeParameters(method, referenceConstraint string) (declaration, arguments string) {
	var declarations, names []string
	if strings.Contains(method, "TReference") {
		declarations = append(declarations, "TReference "+referenceConstraint)
		names = append(names, "TReference")
	}
	if strings.Contains(method, "TMetadata") {
		declarations = append(declarations, "TMetadata model_core.ReferenceMetadata")
		names = append(names, "TMetadata")
	}
	if len(names) == 0 {
		return "", ""
	}
	return "[" + strings.Join(declarations, ", ") + "]", "[" + strings.Join(names, ", ") + "]"
}

// getValueGetterTypeParameters returns the type parameters of the
// interface for obtaining the value of a function.
func (fd functionDefinition) getValueGetterTypeParameters(functionName string) (declaration, arguments string) {
	return getTypeParameters(fd.getValueGetterMethod(functionName), "any")
}

// getObjectGetterTypeParameters returns the type parameters of the
// interface for obtaining the object derived from the value of a
// function.
func (fd functionDefinition) getObjectGetterTypeParameters(functionName string) (declaration, arguments string) {
	referenceConstraint := "any"
	if fd.ObjectType.ReferenceConstraint != "" {
		referenceConstraint = fd.ObjectType.ReferenceConstraint
	}
	return getTypeParameters(fd.getObjectGetterMethod(functionName), referenceConstraint)
}

func main() {
	computerDefinitionData, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal("Failed to read computer definition: ", err)
	}
	var computerDefinition computerDefinition
	if err := json.Unmarshal(computerDefinitionData, &computerDefinition); err != nil {
		log.Fatal("Failed to unmarshal computer definition: ", err)
	}

	// Functions that only have acyclic dependencies are close to
	// the root of the build graph. For these functions we require
	// that it's specified whether the function is computationally
	// expensive, or whether it merely performs a lookup within the
	// values returned by its dependencies.
	//
	// For functions that just perform lookups, it generally doesn't
	// make sense to cache results. The values returned by the
	// functions below likely have a very high amount of churn.
	//
	// TODO: Maybe the right thing to do is to eliminate the
	// existence of such lookup functions entirely? Get rid of
	// BuildSpecification, and require that clients set
	// DirectoryAccessParameters, etc. manually. This has the
	// downside that it becomes more complex to invoke a build, and
	// reduces flexibility on the analysis side.
	worstCaseHeights := map[string]int{}
	for i := 0; i < len(computerDefinition.Functions); i++ {
		for functionName, functionDefinition := range computerDefinition.Functions {
			for _, dependencyName := range functionDefinition.DependsOn {
				newHeight := len(computerDefinition.Functions)
				if dependencyDefinition := computerDefinition.Functions[dependencyName]; dependencyDefinition.IsLookup == nil || *dependencyDefinition.IsLookup {
					newHeight = worstCaseHeights[dependencyName] + 1
				}
				if worstCaseHeights[functionName] < newHeight {
					worstCaseHeights[functionName] = newHeight
				}
			}
		}
	}
	for functionName, functionDefinition := range computerDefinition.Functions {
		worstCaseHeight := worstCaseHeights[functionName]
		if len(functionDefinition.DependsOn) == 0 {
			if functionDefinition.IsLookup != nil {
				log.Fatalf("Function %s specifies whether it is a lookup, which can only be done for functions with dependencies", functionName)
			}
		} else {
			if (functionDefinition.IsLookup == nil) == (worstCaseHeight < len(computerDefinition.Functions)) {
				log.Fatalf("Functions that only have acyclic dependencies must specify whether they are lookups, which function %s violates", functionName)
			}
		}
	}

	fmt.Printf("package %s\n", computerDefinition.GoPackage)

	imports := map[string]string{}
	for _, functionDefinition := range computerDefinition.Functions {
		if objectType := functionDefinition.ObjectType; objectType != nil {
			for shortName, importPath := range objectType.Imports {
				imports[shortName] = importPath
			}
		}
	}
	fmt.Printf("import (\n")
	fmt.Printf("\t\"context\"\n")
	fmt.Printf("\t\"bonanza.build/pkg/model/evaluation\"\n")
	fmt.Printf("\t\"bonanza.build/pkg/storage/object\"\n")
	fmt.Printf("\tmodel_core \"bonanza.build/pkg/model/core\"\n")
	fmt.Printf("\t\"google.golang.org/protobuf/proto\"\n")
	fmt.Printf("\t\"google.golang.org/protobuf/types/known/anypb\"\n")
	fmt.Printf("\tpb %#v\n", computerDefinition.ProtoPackage)
	for _, shortName := range slices.Sorted(maps.Keys(imports)) {
		fmt.Printf("\t%s %#v\n", shortName, imports[shortName])
	}
	fmt.Printf(")\n")

	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		functionDefinition := computerDefinition.Functions[functionName]
		if functionDefinition.KeyContainsReferences {
			fmt.Printf(
				"type Patched%sKey[TMetadata model_core.ReferenceMetadata] = model_core.PatchedMessage[*pb.%s_Key, TMetadata]\n",
				functionName,
				getMessageName(functionName),
			)
		}
		fmt.Printf(
			"type Patched%sValue[TMetadata model_core.ReferenceMetadata] = model_core.PatchedMessage[*pb.%s_Value, TMetadata]\n",
			functionName,
			getMessageName(functionName),
		)
	}

	fmt.Printf("type Computer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {\n")
	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		functionDefinition := computerDefinition.Functions[functionName]
		fmt.Printf(
			"\tCompute%sValue(context.Context, %s, %sEnvironment[TReference, TMetadata]) (Patched%sValue[TMetadata], error)\n",
			functionName,
			functionDefinition.getKeyType(functionName, false),
			functionName,
			functionName,
		)
		if objectType := functionDefinition.ObjectType; objectType != nil {
			fmt.Printf(
				"\tCompute%sObject(model_core.Message[*pb.%s_Value, TReference]) (%s, error)\n",
				functionName,
				getMessageName(functionName),
				objectType.Type,
			)
		}
	}
	fmt.Printf("}\n")

	// Interfaces containing a single method for obtaining the value
	// of a function. These can be used by helper functions that
	// only depend on a small number of functions.
	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		functionDefinition := computerDefinition.Functions[functionName]
		valueTypeParametersDeclaration, _ := functionDefinition.getValueGetterTypeParameters(functionName)
		fmt.Printf("type %sValueGetter%s interface {\n", functionName, valueTypeParametersDeclaration)
		fmt.Printf("\t%s\n", functionDefinition.getValueGetterMethod(functionName))
		fmt.Printf("}\n")
		if functionDefinition.ObjectType != nil {
			objectTypeParametersDeclaration, _ := functionDefinition.getObjectGetterTypeParameters(functionName)
			fmt.Printf("type %sObjectGetter%s interface {\n", functionName, objectTypeParametersDeclaration)
			fmt.Printf("\t%s\n", functionDefinition.getObjectGetterMethod(functionName))
			fmt.Printf("}\n")
		}
	}

	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		fmt.Printf("type %sEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {\n", functionName)
		functionDefinition := computerDefinition.Functions[functionName]
		for _, dependencyName := range slices.Sorted(slices.Values(functionDefinition.DependsOn)) {
			dependencyDefinition := computerDefinition.Functions[dependencyName]
			_, valueTypeArguments := dependencyDefinition.getValueGetterTypeParameters(dependencyName)
			fmt.Printf("\t%sValueGetter%s\n", dependencyName, valueTypeArguments)
			if dependencyDefinition.ObjectType != nil {
				_, objectTypeArguments := dependencyDefinition.getObjectGetterTypeParameters(dependencyName)
				fmt.Printf("\t%sObjectGetter%s\n", dependencyName, objectTypeArguments)
			}
		}
		fmt.Printf("\tmodel_core.ObjectManager[TReference, TMetadata]\n")
		fmt.Printf("}\n")
	}

	fmt.Printf("type typedEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {\n")
	fmt.Printf("\tevaluation.Environment[TReference, TMetadata]\n")
	fmt.Printf("}\n")
	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		functionDefinition := computerDefinition.Functions[functionName]
		fmt.Printf(
			"func (e *typedEnvironment[TReference, TMetadata]) Get%sValue(key %s) model_core.Message[*pb.%s_Value, TReference] {\n",
			functionName,
			functionDefinition.getKeyType(functionName, true),
			getMessageName(functionName),
		)
		fmt.Printf("\tm := e.Environment.GetValue(%s)\n", functionDefinition.keyToPatchedMessage())
		fmt.Printf("\tif !m.IsSet() {\n")
		fmt.Printf("\t\treturn model_core.Message[*pb.%s_Value, TReference]{}\n", getMessageName(functionName))
		fmt.Printf("\t}\n")
		fmt.Printf("\treturn model_core.Message[*pb.%s_Value, TReference]{\n", getMessageName(functionName))
		fmt.Printf("\t\tMessage: m.Message.(*pb.%s_Value),\n", getMessageName(functionName))
		fmt.Printf("\t\tOutgoingReferences: m.OutgoingReferences,\n")
		fmt.Printf("\t}\n")
		fmt.Printf("}\n")
		if objectType := functionDefinition.ObjectType; objectType != nil {
			fmt.Printf(
				"func (e *typedEnvironment[TReference, TMetadata]) Get%sObject(key %s) (%s, bool) {\n",
				functionName,
				functionDefinition.getKeyType(functionName, true),
				objectType.Type,
			)
			fmt.Printf("\tv, ok := e.Environment.GetObject(%s)\n", functionDefinition.keyToPatchedMessage())
			fmt.Printf("\tif !ok {\n")
			fmt.Printf("\t\treturn nil, false\n")
			fmt.Printf("\t}\n")
			fmt.Printf("\treturn v.(%s), true\n", objectType.Type)
			fmt.Printf("}\n")
		}
	}

	fmt.Printf("var isLookupKeyTypeURLs map[string]struct{}\n")

	fmt.Printf("func init() {\n")

	fmt.Printf("\tisLookupKeys := []proto.Message{\n")
	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		functionDefinition := computerDefinition.Functions[functionName]
		if len(functionDefinition.DependsOn) == 0 || (functionDefinition.IsLookup != nil && *functionDefinition.IsLookup) {
			fmt.Printf("\t\t&pb.%s_Key{},\n", getMessageName(functionName))
		}
	}
	fmt.Printf("\t}\n")
	fmt.Printf("\tisLookupKeyTypeURLs = make(map[string]struct{}, len(isLookupKeys))\n")
	fmt.Printf("\tfor _, m := range isLookupKeys {\n")
	fmt.Printf("\t\ta, _ := anypb.New(m)\n")
	fmt.Printf("\t\t\tisLookupKeyTypeURLs[a.TypeUrl] = struct{}{}\n")
	fmt.Printf("\t}\n")

	fmt.Printf("}\n")

	fmt.Printf("type typedComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {\n")
	fmt.Printf("\tbase Computer[TReference, TMetadata]\n")
	fmt.Printf("}\n")
	fmt.Printf("func NewTypedComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](base Computer[TReference, TMetadata]) evaluation.Computer[TReference, TMetadata] {\n")
	fmt.Printf("\treturn &typedComputer[TReference, TMetadata]{base: base}\n")
	fmt.Printf("}\n")

	fmt.Printf("func (c *typedComputer[TReference, TMetadata]) ComputeValue(ctx context.Context, key model_core.Message[proto.Message, TReference], e evaluation.Environment[TReference, TMetadata]) (model_core.PatchedMessage[proto.Message, TMetadata], error) {\n")
	fmt.Printf("\ttypedE := typedEnvironment[TReference, TMetadata]{Environment: e}\n")
	fmt.Printf("\tswitch typedKey := key.Message.(type) {\n")
	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		functionDefinition := computerDefinition.Functions[functionName]
		fmt.Printf("\tcase *pb.%s_Key:\n", getMessageName(functionName))
		fmt.Printf("\t\tm, err := c.base.Compute%sValue(ctx, %s, &typedE)\n", functionName, functionDefinition.typedKeyToArgument(functionName))
		fmt.Printf("\t\treturn model_core.NewPatchedMessage[proto.Message](m.Message, m.Patcher), err\n")
	}
	fmt.Printf("\tdefault:\n")
	fmt.Printf("\t\tpanic(\"unrecognized key type\")\n")
	fmt.Printf("\t}\n")
	fmt.Printf("}\n")

	fmt.Printf("func (c *typedComputer[TReference, TMetadata]) ComputeObject(value model_core.Message[proto.Message, TReference]) (any, error) {\n")
	fmt.Printf("\tswitch typedValue := value.Message.(type) {\n")
	for _, functionName := range slices.Sorted(maps.Keys(computerDefinition.Functions)) {
		if computerDefinition.Functions[functionName].ObjectType != nil {
			fmt.Printf("\tcase *pb.%s_Value:\n", getMessageName(functionName))
			fmt.Printf("\t\treturn c.base.Compute%sObject(model_core.Message[*pb.%s_Value, TReference]{Message: typedValue, OutgoingReferences: value.OutgoingReferences})\n", functionName, getMessageName(functionName))
		}
	}
	fmt.Printf("\tdefault:\n")
	fmt.Printf("\t\tpanic(\"value type has no object\")\n")
	fmt.Printf("\t}\n")
	fmt.Printf("}\n")

	fmt.Printf("func (typedComputer[TReference, TMetadata]) IsLookup(typeURL string) bool {\n")
	fmt.Printf("\t_, ok := isLookupKeyTypeURLs[typeURL]\n")
	fmt.Printf("\treturn ok\n")
	fmt.Printf("}\n")
}
