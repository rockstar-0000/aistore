#
# Copyright (c) 2022-2024, NVIDIA CORPORATION. All rights reserved.
#

# pylint: disable=protected-access

import os
import tarfile
import unittest

from io import BytesIO

from aistore.sdk.client import Client
from aistore.sdk.obj.object_file import ObjectFile
from aistore.sdk.const import DEFAULT_CHUNK_SIZE
from tests.integration import CLUSTER_ENDPOINT
from tests.utils import create_random_tarballs


class TestObjectFileOps(unittest.TestCase):
    TAR_FILE_DIR = "./generated-tarballs"
    TAR_FILE_PATH = "./generated-tarballs/input-shard-0.tar"
    EXTRACT_PATH = "./extracted-tar-files"
    OBJECT_NAME = "test-tarball.tar"
    BUCKET_NAME = "test-tar-bucket"

    @classmethod
    def setUpClass(cls):
        cls.client = Client(CLUSTER_ENDPOINT)
        cls.bucket = cls.client.bucket(cls.BUCKET_NAME).create(exist_ok=True)

        # Generate a tarball of random files
        if not os.path.exists(cls.TAR_FILE_PATH):
            os.makedirs(os.path.dirname(cls.TAR_FILE_PATH), exist_ok=True)
            # Create a tarball with random files
            create_random_tarballs(
                num_files=100,
                num_extensions=5,
                min_shard_size=5000000,
                dest_dir=cls.TAR_FILE_DIR,
            )

        # Read the generated tarball and upload it to the bucket
        with open(cls.TAR_FILE_PATH, "rb") as f:
            cls.tar_data = f.read()

        # Upload the tarball as an object to the bucket
        cls.bucket.object(cls.OBJECT_NAME).put_content(cls.tar_data)

    @classmethod
    def tearDownClass(cls):
        # Delete the test bucket
        cls.bucket.delete()
        # Remove the generated tar file
        if os.path.exists(cls.TAR_FILE_PATH):
            os.remove(cls.TAR_FILE_PATH)

    def setUp(self):
        self.file_obj = self.bucket.object(self.OBJECT_NAME).get().as_file()

    def tearDown(self):
        # Close the file object if it's still open
        try:
            if self.file_obj:
                self.file_obj.close()
        except ValueError:
            pass

        # Remove extracted files
        if os.path.exists(self.EXTRACT_PATH):
            os.system(f"rm -rf {self.EXTRACT_PATH}")

    def test_initialization(self):
        """Test initialization."""
        self.assertIsInstance(self.file_obj, ObjectFile)
        self.assertEqual(self.file_obj.tell(), 0)
        self.assertTrue(self.file_obj.readable())
        self.assertFalse(self.file_obj.seekable())

    def test_close(self):
        """Test file close."""
        self.file_obj.close()
        self.assertTrue(self.file_obj._closed)
        self.assertEqual(self.file_obj._buffer._buffer, b"")
        self.assertEqual(self.file_obj._chunk_iterator, None)
        self.assertFalse(self.file_obj.readable())
        self.assertFalse(self.file_obj.seekable())
        with self.assertRaises(ValueError):
            self.file_obj.tell()
        with self.assertRaises(ValueError):
            self.file_obj.close()
        with self.assertRaises(ValueError):
            self.file_obj.read()

    def test_read_less_than_chunk_size(self):
        """Test reading less than a chunk size."""
        read_size = DEFAULT_CHUNK_SIZE // 2
        read_data = self.file_obj.read(read_size)
        self.assertEqual(read_data, self.tar_data[:read_size])
        self.assertEqual(self.file_obj.tell(), read_size)

    def test_read_exact_chunk_size(self):
        """Test reading exactly one chunk size."""
        read_size = DEFAULT_CHUNK_SIZE
        read_data = self.file_obj.read(read_size)
        self.assertEqual(read_data, self.tar_data[:read_size])
        self.assertEqual(self.file_obj.tell(), read_size)

    def test_read_more_than_chunk_size(self):
        """Test reading more than a chunk size but less than two chunks."""
        read_size = DEFAULT_CHUNK_SIZE + (DEFAULT_CHUNK_SIZE // 2)
        read_data = self.file_obj.read(read_size)
        self.assertEqual(read_data, self.tar_data[:read_size])
        self.assertEqual(self.file_obj.tell(), read_size)

    def test_read_multiple_chunks_plus_partial_chunk(self):
        """Test reading multiple chunks plus a partial chunk."""
        read_size = DEFAULT_CHUNK_SIZE * 2 + (DEFAULT_CHUNK_SIZE // 4)
        read_data = self.file_obj.read(read_size)
        self.assertEqual(read_data, self.tar_data[:read_size])
        self.assertEqual(self.file_obj.tell(), read_size)

    def test_read_entire_file(self):
        """Test reading the entire file."""
        read_data = self.file_obj.read()
        self.assertEqual(read_data, self.tar_data)

    def test_read_size_zero(self):
        """Test reading with size zero."""
        read_data = self.file_obj.read(0)
        self.assertEqual(read_data, b"")

    def test_read_all_and_validate_tar(self):
        """Test reading the entire tar file and validating its integrity."""
        # Read the entire file
        file_data = self.file_obj.read()
        self.assertEqual(len(file_data), len(self.tar_data))

        # Validate the integrity of the tar file by attempting to open and extract it
        with tarfile.open(fileobj=BytesIO(file_data)) as tar:
            tar.extractall(path=self.EXTRACT_PATH)

    def test_read_fixed_and_validate_tar(self):
        """Test reading the tar file in two halves, combining them, and validating integrity."""
        # Read the first half of the file
        half_size = len(self.tar_data) // 2
        file_data_first_half = self.file_obj.read(half_size)
        self.assertEqual(len(file_data_first_half), half_size)
        self.assertEqual(self.file_obj.tell(), half_size)
        self.assertEqual(file_data_first_half, self.tar_data[:half_size])

        # Read the second half of the file
        file_data_second_half = self.file_obj.read()
        self.assertEqual(len(file_data_second_half), len(self.tar_data) - half_size)
        self.assertEqual(self.file_obj.tell(), len(self.tar_data))
        self.assertEqual(file_data_second_half, self.tar_data[half_size:])

        # Combine the two halves and validate the integrity of the tar file
        file_data = file_data_first_half + file_data_second_half
        self.assertEqual(len(file_data), len(self.tar_data))
        with tarfile.open(fileobj=BytesIO(file_data)) as tar:
            tar.extractall(path=self.EXTRACT_PATH)

    def test_read_fixed_all_and_validate_tar(self):
        """Test reading the entire file using fixed read and validating integrity."""
        file_data = self.file_obj.read(len(self.tar_data))
        self.assertEqual(len(file_data), len(self.tar_data))

        with tarfile.open(fileobj=BytesIO(file_data)) as tar:
            tar.extractall(path=self.EXTRACT_PATH)
