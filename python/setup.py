"""
Setup configuration for simple-workflow Python library
"""

from setuptools import setup, find_packages

with open("README.md", "r", encoding="utf-8") as fh:
    long_description = fh.read()

setup(
    name="simple-workflow",
    version="0.1.0",
    author="Simple-Workflow Contributors",
    description="A generic, durable intent system for asynchronous work",
    long_description=long_description,
    long_description_content_type="text/markdown",
    url="https://github.com/tendant/simple-workflow",
    packages=find_packages(),
    classifiers=[
        "Development Status :: 4 - Beta",
        "Intended Audience :: Developers",
        "Topic :: Software Development :: Libraries",
        "License :: OSI Approved :: MIT License",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
    ],
    python_requires=">=3.9",
    install_requires=[
        "psycopg2-binary>=2.9.0",
    ],
)
