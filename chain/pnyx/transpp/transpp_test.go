package transpp

import (
	"fmt"
	"os"
	"testing"

	"github.com/fxamacker/cbor"
)

func TestExampleStream(t *testing.T) {
	type Animal struct {
		Age    int
		Name   string
		Owners []string
	}
	animal := Animal{Age: 4, Name: "Candy", Owners: []string{"Mary", "Joe"}}

	file, err := os.Create("/var/tmp/test.data")
	defer file.Close()

	enc := cbor.NewEncoder(file, cbor.CanonicalEncOptions())
	err = enc.Encode(animal)
	if err != nil {
		fmt.Println("error:", err)
	}

	// Output:
	// a46341676504644e616d656543616e6479664f776e65727382644d617279634a6f65644d616c65f4
}
func TestExampleUnStream(t *testing.T) {
	type Animal struct {
		Age    int
		Name   string
		Owners []string
		Male   bool
		II     []byte
	}

	//cborData, _ := hex.DecodeString("a36341676504644e616d656543616e6479664f776e65727382644d617279634a6f65")
	var animal Animal
	file, err := os.Open("/var/tmp/test.data")
	defer file.Close()
	// create a decoder
	dec := cbor.NewDecoder(file)

	// decode into empty interface

	err = dec.Decode(&animal)
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Printf("%+v", animal)
	// Output:
	// {Age:4 Name:Candy Owners:[Mary Joe] Male:false}
}
