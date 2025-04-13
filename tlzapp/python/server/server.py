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
from transformers import AutoProcessor, Siglip2VisionModel, Siglip2TextModel, pipeline
import io
import json
import numpy as np
import os
import requests
import sqlite_vec
import sqlite3
import torch

DEVICE = torch.device('cuda' if torch.cuda.is_available() else "cpu")
MODEL = "google/siglip2-so400m-patch16-naflex" # (~4.5 GB)   # huggingface model name

processor = AutoProcessor.from_pretrained(MODEL)
text_model = Siglip2TextModel.from_pretrained(MODEL).to(DEVICE)
vision_model = Siglip2VisionModel.from_pretrained(MODEL).to(DEVICE)
image_classifier = pipeline(task="zero-shot-image-classification", model=MODEL, device=DEVICE)

app = Flask(__name__)

@app.route("/embedding", methods=["QUERY"])
def embedding():
	filename = request.args.get('filename')
	ct = request.headers.get('Content-Type')

	if not ct:
		return "Content-Type header required, even if only specifying a filename", 400

	output = None
	data = request.data

	if filename:
		if request.data:
			return "Cannot specify both a request body and a filename", 400
		max_size = 1024 * 1024 * 100
		with open(filename, 'rb') as file:
			data = file.read(max_size)

	if ct.startswith("image/"):
		image = Image.open(io.BytesIO(data)).convert("RGB") # TODO: Is convert necessary?
		image_inputs = processor(images=image, return_tensors="pt", padding="max_length", max_num_patches=1024).to(DEVICE)
		with torch.no_grad():
			output = vision_model(**image_inputs).pooler_output
	elif ct.startswith("text/"):
		# TODO: this truncates inputs at 64 tokens, which is about 1-2 sentences, or ~15 words.
		# Consider using a different model for longer text (and then shift output into same space)
		# or potentially a sliding window over tokens.
		text_input = processor(text=data.decode('utf-8'), return_tensors="pt", padding="max_length", truncation=True).to(DEVICE)
		with torch.no_grad():
			output = text_model(**text_input).pooler_output
	else:
		return "Content-Type must be image/ or text/ media", 400

	embedding = output.cpu().flatten().numpy()
	embedding_floats = embedding.astype(np.float32)
	embedding_list = embedding_floats.tolist()
	embedding_json = json.dumps(embedding_list)

	return embedding_json

# TODO: Currently, only images are supported... support text too!
@app.route("/classify", methods=["QUERY"])
def classify():
	labels = request.args.getlist('labels')
	imageMapping = request.get_json()

	images = []
	itemIDs = []
	for itemID, filename in imageMapping.items():
		images.append(filename)
		itemIDs.append(itemID)

	outputs = image_classifier(images, candidate_labels=labels)

	# NOTE: The output structure only supports one label right now
	results = {}
	for i, output in enumerate(outputs):
		for labelScore in output:
			itemID = itemIDs[i]
			results[itemID] = labelScore['score']

	# TODO: for development only
	print("RESULTS FOR:", labels, results)

	return results

if __name__ == "__main__":
	app.run(host='127.0.0.1', port=12003)

