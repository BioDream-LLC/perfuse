# FEBRL's synthetic record-linkage datasets (BSD-3-Clause), as recordlinkage ships them. Running the image copies
# dataset3.csv to /work, which the patient index's accuracy test reads (PERFUSE_TEST_FEBRL).
FROM python:3.12-slim
RUN pip install --no-cache-dir recordlinkage==0.16
WORKDIR /work
CMD ["sh", "-c", "cp \"$(python -c 'import recordlinkage, os; print(os.path.dirname(recordlinkage.__file__))')/datasets/febrl/dataset3.csv\" /work/"]
