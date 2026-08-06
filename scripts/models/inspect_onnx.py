import argparse
import hashlib
import json
from pathlib import Path

import onnx


def tensor_shape(value_info: onnx.ValueInfoProto) -> list[int | str | None]:
    dimensions: list[int | str | None] = []
    for dimension in value_info.type.tensor_type.shape.dim:
        if dimension.HasField("dim_value"):
            dimensions.append(dimension.dim_value)
        elif dimension.HasField("dim_param"):
            dimensions.append(dimension.dim_param)
        else:
            dimensions.append(None)
    return dimensions


def describe(value_info: onnx.ValueInfoProto) -> dict[str, object]:
    tensor_type = value_info.type.tensor_type
    return {
        "name": value_info.name,
        "elementType": onnx.TensorProto.DataType.Name(tensor_type.elem_type),
        "shape": tensor_shape(value_info),
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Validate and describe an ONNX model")
    parser.add_argument("--model", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()

    model = onnx.load(str(args.model), load_external_data=False)
    onnx.checker.check_model(model)
    digest = hashlib.sha256(args.model.read_bytes()).hexdigest()
    metadata = {
        "sha256": digest,
        "bytes": args.model.stat().st_size,
        "irVersion": model.ir_version,
        "opsets": [
            {"domain": item.domain, "version": item.version}
            for item in model.opset_import
        ],
        "inputs": [describe(item) for item in model.graph.input],
        "outputs": [describe(item) for item in model.graph.output],
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(metadata, indent=2) + "\n", encoding="ascii")
    print(json.dumps(metadata, indent=2))


if __name__ == "__main__":
    main()
