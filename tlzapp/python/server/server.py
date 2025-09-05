'''
Timelinize
Copyright (c) 2013 Matthew Holt

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published
by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
'''

from flask import Flask, request
from PIL import Image
from transformers import AutoProcessor, AutoModel, pipeline
import io
import json
import numpy as np
import requests
import torch
import time
from argparse import ArgumentParser

# support HEIF/HEIC images
from pillow_heif import register_heif_opener
register_heif_opener()

parser = ArgumentParser()
parser.add_argument("--host", help="Host/interface to bind to")
parser.add_argument("--port", help="Port to serve on", type=int)
parser.add_argument("--pingback", help="Pingback URL to notify parent process when the server is ready")
args = parser.parse_args()

MODEL = "google/siglip2-base-patch16-naflex" # used to use so400m, but it was too big for my 4070
processor = AutoProcessor.from_pretrained(MODEL)

# This helps choose whether to run models on CPU or GPU
# (supports both CUDA/Nvidia, and MPS/Apple), and moves
# models on/off the GPU based on available memory.
class DeviceManager:
	def __init__(self, model_name, retry_cooldown=600):
		self.model_name = model_name
		self.retry_cooldown = retry_cooldown
		self.force_cpu = False
		self.last_oom_time = None

		# Detect preferred device at startup
		if torch.cuda.is_available():
			self.preferred_device = "cuda"
		elif torch.backends.mps.is_available() and torch.backends.mps.is_built():
			self.preferred_device = "mps"
		else:
			self.preferred_device = "cpu"

		self._load_model(force_cpu=False)

	def _load_model(self, force_cpu=False):
		if force_cpu:
			device_map = "cpu"
		elif self.preferred_device == "mps":
			device_map = {"": "mps"}
		else:
			device_map = "auto"  # will pick CUDA if available

		self.model = AutoModel.from_pretrained(
			self.model_name,
			trust_remote_code=True,
			device_map=device_map,
		)

		self.model_device = next(self.model.parameters()).device

		if self.model_device.type == "cuda":
			pipeline_device = 0
		elif self.model_device.type == "mps":
			pipeline_device = "mps"
		else:
			pipeline_device = -1

		self.pipeline = pipeline(
			task="zero-shot-image-classification",
			model=self.model_name,
			device=pipeline_device,
		)
		print(f"✅ Loaded {self.model_name} on {self.model_device} (cpu={force_cpu})")

	def _switch_to_cpu(self):
		if not self.force_cpu:
			print(f"⚠️ Switching to CPU due to OOM on {self.model_device}")
			if self.model_device.type == "cuda":
				torch.cuda.empty_cache()
			self.force_cpu = True
			self.last_oom_time = time.time()
			self._load_model(force_cpu=True)

	def _maybe_try_preferred(self):
		if self.force_cpu and self.preferred_device != "cpu":
			if self.last_oom_time is None:
				return
			elapsed = time.time() - self.last_oom_time
			if elapsed >= self.retry_cooldown:
				print(f"🔄 Retrying {self.preferred_device.upper()} load...")
				try:
					self.force_cpu = False
					self._load_model(force_cpu=False)
					self.last_oom_time = None
				except RuntimeError as e:
					print(f"❌ Retry failed: {e}")
					self._switch_to_cpu()

	def run_model(self, forward_fn, **inputs):
		self._maybe_try_preferred()
		try:
			inputs = {k: v.to(self.model_device) for k, v in inputs.items()}
			with torch.no_grad():
				return forward_fn(**inputs)
		except RuntimeError as e:
			if "out of memory" in str(e).lower():
				self._switch_to_cpu()
				return self.run_model(forward_fn, **inputs)
			raise

	def run_pipeline(self, *args, **kwargs):
		self._maybe_try_preferred()
		try:
			return self.pipeline(*args, **kwargs)
		except RuntimeError as e:
			if "out of memory" in str(e).lower():
				self._switch_to_cpu()
				return self.pipeline(*args, **kwargs)
			raise

device_mgr = DeviceManager(MODEL)


app = Flask(__name__)

@app.route("/health-check")
def healthCheck():
	return ""

@app.route("/embedding", methods=["QUERY"])
def embedding():
	filename = request.args.get("filename")
	ct = request.headers.get("Content-Type")

	if not ct:
		return "Content-Type header required, even if only specifying a filename", 400

	data = request.data
	if filename:
		if request.data:
			return "Cannot specify both a request body and a filename", 400
		max_size = 1024 * 1024 * 100
		with open(filename, "rb") as file:
			data = file.read(max_size)

	if ct.startswith("image/"):
		image = Image.open(io.BytesIO(data)).convert("RGB")
		inputs = processor(
			images=image,
			return_tensors="pt",
			padding="max_length",
			max_num_patches=1024,
		)
		output = device_mgr.run_model(device_mgr.model.get_image_features, **inputs)
	elif ct.startswith("text/"):
		text = data.decode("utf-8")
		inputs = processor(
			text=text,
			return_tensors="pt",
			padding="max_length",
			truncation=True,
		)
		output = device_mgr.run_model(device_mgr.model.get_text_features, **inputs)
	else:
		return "Content-Type must be image/ or text/ media", 400

	embedding = output.cpu().flatten().numpy().astype(np.float32).tolist()
	return json.dumps(embedding)

@app.route("/classify", methods=["QUERY"])
def classify():
	labels = request.args.getlist("labels")
	imageMapping = request.get_json()

	images = []
	itemIDs = []
	for itemID, filename in imageMapping.items():
		images.append(filename)
		itemIDs.append(itemID)

	outputs = device_mgr.run_pipeline(images, candidate_labels=labels)

	results = {}
	for i, output in enumerate(outputs):
		for labelScore in output:
			itemID = itemIDs[i]
			results[itemID] = labelScore["score"]

	print("RESULTS FOR:", labels, results)
	return results

if __name__ == "__main__":
	if args.pingback:
		requests.get(args.pingback)

	app.run(host=args.host or "127.0.0.1", port=args.port or 12003)
