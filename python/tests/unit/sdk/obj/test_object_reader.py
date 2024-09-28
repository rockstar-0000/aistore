import unittest
from unittest.mock import patch, Mock
import requests

from aistore.sdk.obj.content_iterator import ContentIterator
from aistore.sdk.obj.object_file import ObjectFile
from aistore.sdk.obj.object_reader import ObjectReader
from aistore.sdk.obj.object_attributes import ObjectAttributes


class TestObjectReader(unittest.TestCase):
    def setUp(self):
        self.object_client = Mock()
        self.chunk_size = 1024
        self.object_reader = ObjectReader(self.object_client, self.chunk_size)
        self.response_headers = {"attr1": "resp1", "attr2": "resp2"}

    def test_head(self):
        mock_attr = Mock()
        self.object_client.head.return_value = mock_attr

        res = self.object_reader.head()

        # Attributes should be returned and the property updated
        self.assertEqual(res, mock_attr)
        self.assertEqual(mock_attr, self.object_reader.attributes)
        self.object_client.head.assert_called_once()

    def test_attributes_property(self):
        mock_attr = Mock()
        self.object_client.head.return_value = mock_attr

        attr = self.object_reader.attributes

        # Attributes should be returned and the property updated
        self.assertEqual(attr, mock_attr)
        # If we access attributes again, no new call to the client
        attr = self.object_reader.attributes
        self.assertEqual(attr, mock_attr)
        self.object_client.head.assert_called_once()

    @patch("aistore.sdk.obj.object_reader.ObjectAttributes", autospec=True)
    def test_read_all(self, mock_attr):
        # Should return the response content and update the attributes
        chunk1 = b"chunk1"
        chunk2 = b"chunk2"
        mock_response = Mock(
            spec=requests.Response,
            content=chunk1 + chunk2,
            headers=self.response_headers,
        )
        self.object_client.get.return_value = mock_response

        content = self.object_reader.read_all()

        # Assert the result, the call to object client
        self.assertEqual(chunk1 + chunk2, content)
        self.object_client.get.assert_called_with(stream=False, start_position=0)
        # Assert attributes parsed and updated
        self.assertIsInstance(self.object_reader.attributes, ObjectAttributes)
        mock_attr.assert_called_with(self.response_headers)

    @patch("aistore.sdk.obj.object_reader.ObjectAttributes", autospec=True)
    def test_raw(self, mock_attr):
        mock_response = Mock(
            spec=requests.Response, raw=b"bytestream", headers=self.response_headers
        )
        self.object_client.get.return_value = mock_response

        raw_stream = self.object_reader.raw()

        # Assert the result, the call to object client
        self.assertEqual(mock_response.raw, raw_stream)
        self.object_client.get.assert_called_with(stream=True, start_position=0)
        # Assert attributes parsed and updated
        self.assertIsInstance(self.object_reader.attributes, ObjectAttributes)
        mock_attr.assert_called_with(self.response_headers)

    @patch("aistore.sdk.obj.object_reader.ContentIterator")
    def test_iter(self, mock_cont_iter_class):
        mock_cont_iter, iterable_bytes = self.setup_mock_iterator(mock_cont_iter_class)

        res = iter(self.object_reader)

        mock_cont_iter.iter_from_position.assert_called_with(0)
        self.assertEqual(iterable_bytes, res)

    @patch("aistore.sdk.obj.object_reader.ContentIterator")
    def test_iter_start_position(self, mock_cont_iter_class):
        mock_cont_iter, iterable_bytes = self.setup_mock_iterator(mock_cont_iter_class)
        start_position = 2048

        res = self.object_reader.iter_from_position(start_position)

        mock_cont_iter.iter_from_position.assert_called_with(start_position)
        self.assertEqual(iterable_bytes, res)

    def setup_mock_iterator(self, mock_cont_iter_class):
        # We patch the class, so use it to create a new instance of a mock content iterator
        mock_cont_iter = Mock()
        iterable_bytes = iter(b"test")
        mock_cont_iter.iter_from_position.return_value = iterable_bytes
        mock_cont_iter_class.return_value = mock_cont_iter
        # Re-create to use the patched ContentIterator in constructor
        self.object_reader = ObjectReader(self.object_client)
        return mock_cont_iter, iterable_bytes

    @patch("aistore.sdk.obj.object_reader.ObjectFile", autospec=True)
    def test_as_file(self, mock_obj_file):
        # Returns an object file with the default resume count
        res = self.object_reader.as_file()
        self.assertIsInstance(res, ObjectFile)
        mock_obj_file.assert_called_once()
        # Get the arguments passed to the mock
        args, kwargs = mock_obj_file.call_args
        # For now just check that we provided a content iterator
        self.assertIsInstance(args[0], ContentIterator)
        # Check the max_resume argument
        self.assertEqual(kwargs.get("max_resume"), 5)

    @patch("aistore.sdk.obj.object_reader.ObjectFile", autospec=True)
    def test_as_file_max_resume(self, mock_obj_file):
        max_resume = 12
        # Returns an object file with the default resume count
        res = self.object_reader.as_file(max_resume=max_resume)
        self.assertIsInstance(res, ObjectFile)
        mock_obj_file.assert_called_once()
        # Get the arguments passed to the mock
        args, kwargs = mock_obj_file.call_args
        # For now just check that we provided a content iterator
        self.assertIsInstance(args[0], ContentIterator)
        # Check the max_resume argument
        self.assertEqual(kwargs.get("max_resume"), max_resume)
