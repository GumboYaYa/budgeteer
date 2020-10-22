from rest_framework import serializers
from rest_framework import fields
from .models import Transaction


class TransactionSerializer(serializers.HyperlinkedModelSerializer):
    class Meta:
        model = Transaction
        fields = ["date_booking", "date_imported"]
        